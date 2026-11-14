package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"
)

type liabilityPurchaseInput struct {
	LiabilityAccountID string     `json:"liability_account_id"`
	PrincipalAmountIDR int64      `json:"principal_amount_idr"`
	TotalFeeIDR        int64      `json:"total_fee_idr"`
	Tenor              int        `json:"tenor"`
	FirstDueDate       string     `json:"first_due_date"`
	CategoryID         string     `json:"category_id,omitempty"`
	Description        string     `json:"description"`
	OccurredAt         *time.Time `json:"occurred_at,omitempty"`
}

func parseDueDate(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, fmt.Errorf("first_due_date is required")
	}
	return time.Parse("2006-01-02", s)
}

func splitInstallments(total int64, tenor int) []int64 {
	parts := make([]int64, tenor)
	base, remainder := total/int64(tenor), total%int64(tenor)
	for i := range parts {
		parts[i] = base
		if int64(i)+1 <= remainder {
			parts[i]++
		}
	}
	return parts
}

func (a *App) financeAccountGet(w http.ResponseWriter, r *http.Request) {
	q := `SELECT a.id::text AS id,a.name,a.kind,a.classification,a.opening_balance_idr,a.credit_limit_idr,
 (a.opening_balance_idr + coalesce((SELECT sum(CASE WHEN a.classification='asset' AND t.to_account_id=a.id THEN t.amount_idr WHEN a.classification='asset' AND t.from_account_id=a.id THEN -t.amount_idr WHEN a.classification='liability' AND t.to_account_id=a.id AND t.type IN ('liability_purchase','liability_fee') THEN t.amount_idr WHEN a.classification='liability' AND t.to_account_id=a.id AND t.type='debt_repayment' THEN -t.amount_idr ELSE 0 END) FROM mimo.finance_transactions t WHERE t.owner_id=$1 AND t.status='posted' AND (t.to_account_id=a.id OR t.from_account_id=a.id)),0)) AS balance_idr,
 CASE WHEN a.classification='liability' AND a.credit_limit_idr IS NOT NULL THEN a.credit_limit_idr - (a.opening_balance_idr + coalesce((SELECT sum(CASE WHEN t.to_account_id=a.id AND t.type IN ('liability_purchase','liability_fee') THEN t.amount_idr WHEN t.to_account_id=a.id AND t.type='debt_repayment' THEN -t.amount_idr ELSE 0 END) FROM mimo.finance_transactions t WHERE t.owner_id=$1 AND t.status='posted' AND (t.to_account_id=a.id OR t.from_account_id=a.id)),0)) END AS available_credit_idr
 FROM mimo.finance_accounts a WHERE a.owner_id=$1 AND a.id=$2::uuid AND a.archived_at IS NULL`
	out, err := one(r.Context(), a.DB, q, a.owner(r.Context()), id(r))
	if isNotFound(err) {
		jsonErr(w, 404, "account not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid account id")
		return
	}
	jsonOK(w, out)
}

func installmentStatus(due time.Time, principalDue, feeDue, principalPaid, feePaid int64) string {
	if principalPaid == principalDue && feePaid == feeDue {
		return "paid"
	}
	if principalPaid > 0 || feePaid > 0 {
		return "partial"
	}
	if due.Before(time.Now().UTC()) {
		return "overdue"
	}
	return "pending"
}

func (a *App) financeLiabilityPurchase(w http.ResponseWriter, r *http.Request) {
	var d liabilityPurchaseInput
	if !decode(w, r, &d) {
		return
	}
	if d.PrincipalAmountIDR <= 0 || d.TotalFeeIDR < 0 || d.Tenor < 1 || d.Tenor > 120 || len(d.Description) > 2000 || d.LiabilityAccountID == "" {
		jsonErr(w, 400, "invalid purchase fields")
		return
	}
	due, err := parseDueDate(d.FirstDueDate)
	if err != nil {
		jsonErr(w, 400, "first_due_date must be YYYY-MM-DD")
		return
	}
	if d.CategoryID != "" {
		var ok bool
		if err = a.DB.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mimo.finance_categories WHERE owner_id=$1 AND id=$2::uuid AND kind='expense')`, a.owner(r.Context()), d.CategoryID).Scan(&ok); err != nil || !ok {
			jsonErr(w, 400, "invalid expense category")
			return
		}
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) > 120 {
		jsonErr(w, 400, "idempotency key too long")
		return
	}
	raw, _ := json.Marshal(d)
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var classification string
	if err = tx.QueryRow(ctx, `SELECT classification FROM mimo.finance_accounts WHERE owner_id=$1 AND id=$2::uuid AND archived_at IS NULL FOR UPDATE`, a.owner(ctx), d.LiabilityAccountID).Scan(&classification); err != nil || classification != "liability" {
		jsonErr(w, 400, "liability account not found")
		return
	}
	if idem != "" {
		var priorID, priorHash string
		if e := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM mimo.finance_transactions WHERE owner_id=$1 AND idempotency_key=$2`, a.owner(ctx), idem).Scan(&priorID, &priorHash); e == nil {
			if priorHash != hash {
				jsonErr(w, 409, "idempotency key reused with different purchase")
				return
			}
			_ = tx.Commit(ctx)
			out, _ := one(ctx, a.DB, transactionSelect+` AND id=$2::uuid`, a.owner(ctx), priorID)
			jsonOK(w, out)
			return
		}
	}
	var purchaseID string
	if err = tx.QueryRow(ctx, `INSERT INTO mimo.finance_transactions(owner_id,type,amount_idr,to_account_id,category_id,description,occurred_at,idempotency_key,request_hash) VALUES($1,'liability_purchase',$2,$3::uuid,$4::uuid,$5,coalesce($6,now()),$7,$8) RETURNING id::text`, a.owner(ctx), d.PrincipalAmountIDR, d.LiabilityAccountID, nullUUID(d.CategoryID), d.Description, d.OccurredAt, nullUUID(idem), hash).Scan(&purchaseID); err != nil {
		internalErr(w, err)
		return
	}
	var debtID string
	if err = tx.QueryRow(ctx, `INSERT INTO mimo.finance_debts(owner_id,liability_account_id,purchase_transaction_id,principal_idr,total_fee_idr,tenor,first_due_date) VALUES($1,$2::uuid,$3::uuid,$4,$5,$6,$7) RETURNING id::text`, a.owner(ctx), d.LiabilityAccountID, purchaseID, d.PrincipalAmountIDR, d.TotalFeeIDR, d.Tenor, due).Scan(&debtID); err != nil {
		internalErr(w, err)
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE mimo.finance_transactions SET debt_id=$1::uuid WHERE id=$2::uuid`, debtID, purchaseID); err != nil {
		internalErr(w, err)
		return
	}
	if d.TotalFeeIDR > 0 {
		if _, err = tx.Exec(ctx, `INSERT INTO mimo.finance_transactions(owner_id,type,amount_idr,to_account_id,description,occurred_at,debt_id,parent_transaction_id) VALUES($1,'liability_fee',$2,$3::uuid,$4,coalesce($5,now()),$6::uuid,$7::uuid)`, a.owner(ctx), d.TotalFeeIDR, d.LiabilityAccountID, d.Description+" (fee)", d.OccurredAt, debtID, purchaseID); err != nil {
			internalErr(w, err)
			return
		}
	}
	principalParts, feeParts := splitInstallments(d.PrincipalAmountIDR, d.Tenor), splitInstallments(d.TotalFeeIDR, d.Tenor)
	for n := 1; n <= d.Tenor; n++ {
		principal, fee := principalParts[n-1], feeParts[n-1]
		if _, err = tx.Exec(ctx, `INSERT INTO mimo.finance_installments(debt_id,installment_number,due_date,principal_due_idr,fee_due_idr) VALUES($1::uuid,$2,$3,$4,$5)`, debtID, n, due.AddDate(0, n-1, 0), principal, fee); err != nil {
			internalErr(w, err)
			return
		}
	}
	if err = a.audit(ctx, tx, "finance.liability_purchase", purchaseID); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, `SELECT d.id::text AS id,d.principal_idr,d.total_fee_idr,d.tenor,d.first_due_date,d.status,t.id::text AS purchase_transaction_id FROM mimo.finance_debts d JOIN mimo.finance_transactions t ON t.id=d.purchase_transaction_id WHERE d.owner_id=$1 AND d.id=$2::uuid`, a.owner(ctx), debtID)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}

func (a *App) financeInstallments(w http.ResponseWriter, r *http.Request) {
	q := `SELECT i.id::text AS id,i.debt_id::text AS debt_id,i.installment_number,i.due_date,i.principal_due_idr,i.fee_due_idr,i.principal_paid_idr,i.fee_paid_idr,CASE WHEN i.principal_paid_idr=i.principal_due_idr AND i.fee_paid_idr=i.fee_due_idr THEN 'paid' WHEN i.principal_paid_idr>0 OR i.fee_paid_idr>0 THEN 'partial' WHEN i.due_date < CURRENT_DATE THEN 'overdue' ELSE 'pending' END AS status,(i.principal_due_idr+i.fee_due_idr-i.principal_paid_idr-i.fee_paid_idr) AS remaining_idr FROM mimo.finance_installments i JOIN mimo.finance_debts d ON d.id=i.debt_id WHERE d.owner_id=$1 AND d.status='open'`
	args := []any{a.owner(r.Context())}
	where := ""
	if v := r.URL.Query().Get("debt_id"); v != "" {
		where = " AND d.id=$2::uuid"
		args = append(args, v)
	}
	if r.URL.Query().Get("upcoming") == "true" {
		where += " AND i.due_date >= CURRENT_DATE"
	}
	if r.URL.Query().Get("overdue") == "true" {
		where += " AND i.due_date < CURRENT_DATE AND (i.principal_paid_idr<i.principal_due_idr OR i.fee_paid_idr<i.fee_due_idr)"
	}
	data, err := many(r.Context(), a.DB, q+where+" ORDER BY i.due_date,i.installment_number LIMIT $"+strconv.Itoa(len(args)+1), append(args, limit(r, 100, 500))...)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}

type installmentPaymentInput struct {
	AssetAccountID string `json:"asset_account_id"`
	AmountIDR      int64  `json:"amount_idr"`
	Description    string `json:"description"`
}

func (a *App) financeInstallmentPay(w http.ResponseWriter, r *http.Request) {
	var d installmentPaymentInput
	if !decode(w, r, &d) {
		return
	}
	if d.AssetAccountID == "" || d.AmountIDR <= 0 || len(d.Description) > 2000 {
		jsonErr(w, 400, "invalid payment fields")
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) > 120 {
		jsonErr(w, 400, "idempotency key too long")
		return
	}
	raw, _ := json.Marshal(struct {
		ID    string
		Input installmentPaymentInput
	}{id(r), d})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if idem != "" {
		var priorID, priorHash string
		if e := tx.QueryRow(ctx, `SELECT id::text,request_hash FROM mimo.finance_transactions WHERE owner_id=$1 AND idempotency_key=$2`, a.owner(ctx), idem).Scan(&priorID, &priorHash); e == nil {
			if priorHash != hash {
				jsonErr(w, 409, "idempotency key reused with different payment")
				return
			}
			_ = tx.Commit(ctx)
			out, _ := one(ctx, a.DB, transactionSelect+` AND id=$2::uuid`, a.owner(ctx), priorID)
			jsonOK(w, out)
			return
		}
	}
	var debtID, liabilityID string
	var principalDue, feeDue, principalPaid, feePaid int64
	if err = tx.QueryRow(ctx, `SELECT d.id::text,d.liability_account_id::text,i.principal_due_idr,i.fee_due_idr,i.principal_paid_idr,i.fee_paid_idr FROM mimo.finance_installments i JOIN mimo.finance_debts d ON d.id=i.debt_id WHERE i.id=$1::uuid AND d.owner_id=$2 AND d.status='open' FOR UPDATE`, id(r), a.owner(ctx)).Scan(&debtID, &liabilityID, &principalDue, &feeDue, &principalPaid, &feePaid); err != nil {
		jsonErr(w, 404, "installment not found")
		return
	}
	var assetClass string
	if err = tx.QueryRow(ctx, `SELECT classification FROM mimo.finance_accounts WHERE id=$1::uuid AND owner_id=$2 AND archived_at IS NULL`, d.AssetAccountID, a.owner(ctx)).Scan(&assetClass); err != nil || assetClass != "asset" {
		jsonErr(w, 400, "asset account not found")
		return
	}
	var outstanding int64
	if err = tx.QueryRow(ctx, `SELECT coalesce(sum(principal_due_idr+fee_due_idr-principal_paid_idr-fee_paid_idr),0) FROM mimo.finance_installments WHERE debt_id=$1::uuid AND (principal_paid_idr<principal_due_idr OR fee_paid_idr<fee_due_idr)`, debtID).Scan(&outstanding); err != nil {
		internalErr(w, err)
		return
	}
	if d.AmountIDR > outstanding {
		jsonErr(w, 400, "payment exceeds outstanding balance")
		return
	}
	var txID string
	if err = tx.QueryRow(ctx, `INSERT INTO mimo.finance_transactions(owner_id,type,amount_idr,from_account_id,to_account_id,description,idempotency_key,request_hash,debt_id) VALUES($1,'debt_repayment',$2,$3::uuid,$4::uuid,$5,$6,$7,$8::uuid) RETURNING id::text`, a.owner(ctx), d.AmountIDR, d.AssetAccountID, liabilityID, d.Description, nullUUID(idem), hash, debtID).Scan(&txID); err != nil {
		internalErr(w, err)
		return
	}
	remaining := d.AmountIDR
	rows, err := tx.Query(ctx, `SELECT id::text,principal_due_idr,fee_due_idr,principal_paid_idr,fee_paid_idr FROM mimo.finance_installments WHERE debt_id=$1::uuid AND (principal_paid_idr<principal_due_idr OR fee_paid_idr<fee_due_idr) ORDER BY installment_number FOR UPDATE`, debtID)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	for rows.Next() && remaining > 0 {
		var iid string
		var pd, fd, pp, fp int64
		if err = rows.Scan(&iid, &pd, &fd, &pp, &fp); err != nil {
			internalErr(w, err)
			return
		}
		pRemain := pd - pp
		pPay := pRemain
		if pPay > remaining {
			pPay = remaining
		}
		remaining -= pPay
		fRemain := fd - fp
		fPay := fRemain
		if fPay > remaining {
			fPay = remaining
		}
		remaining -= fPay
		if _, err = tx.Exec(ctx, `INSERT INTO mimo.finance_installment_payments(installment_id,transaction_id,principal_paid_idr,fee_paid_idr) VALUES($1::uuid,$2::uuid,$3,$4)`, iid, txID, pPay, fPay); err != nil {
			internalErr(w, err)
			return
		}
		status := "partial"
		if pp+pPay == pd && fp+fPay == fd {
			status = "paid"
		}
		if _, err = tx.Exec(ctx, `UPDATE mimo.finance_installments SET principal_paid_idr=principal_paid_idr+$1,fee_paid_idr=fee_paid_idr+$2,status=$3 WHERE id=$4::uuid`, pPay, fPay, status, iid); err != nil {
			internalErr(w, err)
			return
		}
	}
	if remaining != 0 {
		internalErr(w, fmt.Errorf("payment allocation mismatch"))
		return
	}
	if _, err = tx.Exec(ctx, `UPDATE mimo.finance_debts SET status='paid' WHERE id=$1::uuid AND NOT EXISTS (SELECT 1 FROM mimo.finance_installments WHERE debt_id=$1::uuid AND (principal_paid_idr<principal_due_idr OR fee_paid_idr<fee_due_idr))`, debtID); err != nil {
		internalErr(w, err)
		return
	}
	if err = a.audit(ctx, tx, "finance.debt_repayment", txID); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, transactionSelect+` AND id=$2::uuid`, a.owner(ctx), txID)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
