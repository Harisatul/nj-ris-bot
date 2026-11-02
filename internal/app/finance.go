package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/mimo/personal-os/internal/config"
)

type moneyInput struct {
	Type          string     `json:"type"`
	AmountIDR     int64      `json:"amount_idr"`
	FromAccountID string     `json:"from_account_id,omitempty"`
	ToAccountID   string     `json:"to_account_id,omitempty"`
	CategoryID    string     `json:"category_id,omitempty"`
	Description   string     `json:"description"`
	OccurredAt    *time.Time `json:"occurred_at,omitempty"`
}

func validateMoney(d moneyInput) string {
	if d.AmountIDR <= 0 || d.AmountIDR > 1_000_000_000_000 {
		return "amount_idr must be >0 and <=1 trillion"
	}
	if len(d.Description) > 2000 {
		return "description too long"
	}
	switch d.Type {
	case "expense":
		if d.FromAccountID == "" || d.ToAccountID != "" {
			return "expense requires from_account_id only"
		}
	case "income":
		if d.ToAccountID == "" || d.FromAccountID != "" {
			return "income requires to_account_id only"
		}
	case "transfer":
		if d.FromAccountID == "" || d.ToAccountID == "" || d.FromAccountID == d.ToAccountID || d.CategoryID != "" {
			return "transfer needs different accounts and no category"
		}
	default:
		return "type must be income, expense, or transfer"
	}
	return ""
}

const transactionSelect = `SELECT id::text AS id,type,amount_idr,from_account_id::text AS from_account_id,to_account_id::text AS to_account_id,category_id::text AS category_id,description,occurred_at,status,created_at,updated_at FROM mimo.finance_transactions WHERE owner_id=$1`

func (a *App) financeAccountCreate(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Name              string `json:"name"`
		Kind              string `json:"kind"`
		OpeningBalanceIDR int64  `json:"opening_balance_idr"`
	}
	if !decode(w, r, &d) {
		return
	}
	if !requireText(d.Name, 120) {
		jsonErr(w, 400, "invalid account name")
		return
	}
	switch d.Kind {
	case "cash", "bank", "ewallet", "other":
	default:
		jsonErr(w, 400, "invalid account kind")
		return
	}
	var rid string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.finance_accounts(owner_id,name,kind,opening_balance_idr) VALUES($1,$2,$3,$4) RETURNING id::text`, a.C.OwnerID, strings.TrimSpace(d.Name), d.Kind, d.OpeningBalanceIDR).Scan(&rid)
	if err != nil {
		jsonErr(w, 409, "account name already exists or invalid")
		return
	}
	out, err := one(r.Context(), a.DB, `SELECT id::text AS id,name,kind,opening_balance_idr FROM mimo.finance_accounts WHERE owner_id=$1 AND id=$2::uuid`, a.C.OwnerID, rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
func (a *App) financeAccounts(w http.ResponseWriter, r *http.Request) {
	q := `SELECT a.id::text AS id,a.name,a.kind,a.opening_balance_idr,
 (a.opening_balance_idr + coalesce((SELECT sum(CASE
 WHEN t.to_account_id=a.id THEN t.amount_idr ELSE -t.amount_idr END)
 FROM mimo.finance_transactions t WHERE t.owner_id=$1 AND t.status='posted'
 AND (t.to_account_id=a.id OR t.from_account_id=a.id)),0)) AS balance_idr
 FROM mimo.finance_accounts a WHERE a.owner_id=$1 AND a.archived_at IS NULL ORDER BY a.name`
	data, err := many(r.Context(), a.DB, q, a.C.OwnerID)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) financeCategoryCreate(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Name string `json:"name"`
		Kind string `json:"kind"`
	}
	if !decode(w, r, &d) {
		return
	}
	if !requireText(d.Name, 120) || (d.Kind != "income" && d.Kind != "expense") {
		jsonErr(w, 400, "invalid category")
		return
	}
	var rid string
	err := a.DB.QueryRow(r.Context(), `INSERT INTO mimo.finance_categories(owner_id,name,kind) VALUES($1,$2,$3) ON CONFLICT(owner_id,name,kind) DO UPDATE SET name=EXCLUDED.name RETURNING id::text`, a.C.OwnerID, strings.TrimSpace(d.Name), d.Kind).Scan(&rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(r.Context(), a.DB, `SELECT id::text AS id,name,kind FROM mimo.finance_categories WHERE owner_id=$1 AND id=$2::uuid`, a.C.OwnerID, rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
func (a *App) financeCategories(w http.ResponseWriter, r *http.Request) {
	data, err := many(r.Context(), a.DB, `SELECT id::text AS id,name,kind FROM mimo.finance_categories WHERE owner_id=$1 ORDER BY kind,name`, a.C.OwnerID)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) validMoneyRefs(ctxOwner string, d moneyInput, tx pgx.Tx, r *http.Request) error {
	for _, acc := range []string{d.FromAccountID, d.ToAccountID} {
		if acc == "" {
			continue
		}
		var ok bool
		if err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mimo.finance_accounts WHERE id=$1::uuid AND owner_id=$2 AND archived_at IS NULL)`, acc, ctxOwner).Scan(&ok); err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("account %s does not belong to owner", acc)
		}
	}
	if d.CategoryID != "" {
		var ok bool
		err := tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM mimo.finance_categories WHERE id=$1::uuid AND owner_id=$2 AND kind=$3)`, d.CategoryID, ctxOwner, d.Type).Scan(&ok)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("category mismatch or not found")
		}
	}
	return nil
}
func nullUUID(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func (a *App) financeTransactionCreate(w http.ResponseWriter, r *http.Request) {
	var d moneyInput
	if !decode(w, r, &d) {
		return
	}
	if why := validateMoney(d); why != "" {
		jsonErr(w, 400, why)
		return
	}
	idem := r.Header.Get("Idempotency-Key")
	if len(idem) > 120 {
		jsonErr(w, 400, "idempotency key too long")
		return
	}
	raw, _ := json.Marshal(d)
	digest := sha256.Sum256(raw)
	hash := hex.EncodeToString(digest[:])
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	if err = a.validMoneyRefs(a.C.OwnerID, d, tx, r); err != nil {
		jsonErr(w, 400, "invalid account or category reference")
		return
	}
	var rid string
	var key any
	if idem != "" {
		key = idem
	}
	err = tx.QueryRow(ctx, `INSERT INTO mimo.finance_transactions(owner_id,type,amount_idr,from_account_id,to_account_id,category_id,description,occurred_at,idempotency_key,request_hash)
 VALUES($1,$2,$3,$4::uuid,$5::uuid,$6::uuid,$7,coalesce($8,now()),$9,$10)
 ON CONFLICT DO NOTHING RETURNING id::text`, a.C.OwnerID, d.Type, d.AmountIDR, nullUUID(d.FromAccountID), nullUUID(d.ToAccountID), nullUUID(d.CategoryID), d.Description, d.OccurredAt, key, hash).Scan(&rid)
	if isNotFound(err) && idem != "" {
		var prevHash string
		err = tx.QueryRow(ctx, `SELECT id::text,request_hash FROM mimo.finance_transactions WHERE owner_id=$1 AND idempotency_key=$2`, a.C.OwnerID, idem).Scan(&rid, &prevHash)
		if err == nil && prevHash != hash {
			jsonErr(w, 409, "idempotency key reused with a different transaction")
			return
		}
		if err == nil {
			_ = tx.Commit(ctx)
			out, _ := one(ctx, a.DB, transactionSelect+` AND id=$2::uuid`, a.C.OwnerID, rid)
			jsonOK(w, out)
			return
		}
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	if err = a.audit(ctx, tx, "finance.create", rid); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, transactionSelect+` AND id=$2::uuid`, a.C.OwnerID, rid)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonCreated(w, out)
}
func (a *App) financeTransactions(w http.ResponseWriter, r *http.Request) {
	q := transactionSelect + ` AND status='posted' ORDER BY occurred_at DESC,id DESC LIMIT $2`
	data, err := many(r.Context(), a.DB, q, a.C.OwnerID, limit(r, 50, 200))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"items": data})
}
func (a *App) financeTransactionGet(w http.ResponseWriter, r *http.Request) {
	data, err := one(r.Context(), a.DB, transactionSelect+` AND id=$2::uuid`, a.C.OwnerID, id(r))
	if isNotFound(err) {
		jsonErr(w, 404, "transaction not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid transaction id")
		return
	}
	jsonOK(w, data)
}
func (a *App) financeTransactionPatch(w http.ResponseWriter, r *http.Request) {
	var patch struct {
		AmountIDR   *int64     `json:"amount_idr"`
		Description *string    `json:"description"`
		CategoryID  *string    `json:"category_id"`
		OccurredAt  *time.Time `json:"occurred_at"`
	}
	if !decode(w, r, &patch) {
		return
	}
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	var old moneyInput
	var from, to, cat string
	var occurred time.Time
	err = tx.QueryRow(ctx, `SELECT type,amount_idr,coalesce(from_account_id::text,''),coalesce(to_account_id::text,''),coalesce(category_id::text,''),description,occurred_at
 FROM mimo.finance_transactions WHERE owner_id=$1 AND id=$2::uuid AND status='posted' FOR UPDATE`, a.C.OwnerID, id(r)).Scan(&old.Type, &old.AmountIDR, &from, &to, &cat, &old.Description, &occurred)
	if isNotFound(err) {
		jsonErr(w, 404, "posted transaction not found")
		return
	}
	if err != nil {
		jsonErr(w, 400, "invalid transaction id")
		return
	}
	old.FromAccountID = from
	old.ToAccountID = to
	old.CategoryID = cat
	old.OccurredAt = &occurred
	if patch.AmountIDR != nil {
		old.AmountIDR = *patch.AmountIDR
	}
	if patch.Description != nil {
		old.Description = *patch.Description
	}
	if patch.CategoryID != nil {
		old.CategoryID = *patch.CategoryID
	}
	if patch.OccurredAt != nil {
		old.OccurredAt = patch.OccurredAt
	}
	if why := validateMoney(old); why != "" {
		jsonErr(w, 400, why)
		return
	}
	if err = a.validMoneyRefs(a.C.OwnerID, old, tx, r); err != nil {
		jsonErr(w, 400, "invalid category")
		return
	}
	_, err = tx.Exec(ctx, `INSERT INTO mimo.finance_transaction_revisions(transaction_id,snapshot) SELECT id,to_jsonb(t) FROM mimo.finance_transactions t WHERE t.owner_id=$1 AND t.id=$2::uuid`, a.C.OwnerID, id(r))
	if err == nil {
		_, err = tx.Exec(ctx, `UPDATE mimo.finance_transactions SET amount_idr=$1,description=$2,category_id=$3::uuid,occurred_at=$4 WHERE owner_id=$5 AND id=$6::uuid`, old.AmountIDR, old.Description, nullUUID(old.CategoryID), old.OccurredAt, a.C.OwnerID, id(r))
	}
	if err == nil {
		err = a.audit(ctx, tx, "finance.update", id(r))
	}
	if err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	out, err := one(ctx, a.DB, transactionSelect+` AND id=$2::uuid`, a.C.OwnerID, id(r))
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, out)
}
func (a *App) financeTransactionDelete(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tx, err := a.DB.Begin(ctx)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE mimo.finance_transactions SET status='voided' WHERE owner_id=$1 AND id=$2::uuid AND status='posted'`, a.C.OwnerID, id(r))
	if err != nil {
		jsonErr(w, 400, "invalid transaction id")
		return
	}
	if tag.RowsAffected() == 0 {
		jsonErr(w, 404, "posted transaction not found")
		return
	}
	if err = a.audit(ctx, tx, "finance.void", id(r)); err == nil {
		err = tx.Commit(ctx)
	}
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"voided": true})
}
func (a *App) financeSummary(w http.ResponseWriter, r *http.Request) {
	now := time.Now().In(config.WIB())
	start := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, config.WIB())
	end := start.AddDate(0, 1, 0)
	for _, p := range []struct {
		key    string
		target *time.Time
	}{{"from", &start}, {"to", &end}} {
		if v := r.URL.Query().Get(p.key); v != "" {
			d, err := time.ParseInLocation("2006-01-02", v, config.WIB())
			if err != nil {
				jsonErr(w, 400, "dates must be YYYY-MM-DD")
				return
			}
			*p.target = d
		}
	}
	if !end.After(start) {
		jsonErr(w, 400, "to must be after from (exclusive)")
		return
	}
	var expense, income, transfers int64
	err := a.DB.QueryRow(r.Context(), `SELECT coalesce(sum(amount_idr) FILTER (WHERE type='expense'),0),coalesce(sum(amount_idr) FILTER (WHERE type='income'),0),coalesce(sum(amount_idr) FILTER (WHERE type='transfer'),0)
 FROM mimo.finance_transactions WHERE owner_id=$1 AND status='posted' AND occurred_at >= $2 AND occurred_at < $3`, a.C.OwnerID, start, end).Scan(&expense, &income, &transfers)
	if err != nil {
		internalErr(w, err)
		return
	}
	cats, err := many(r.Context(), a.DB, `SELECT coalesce(c.name,'Uncategorized') AS category,t.type,sum(t.amount_idr) AS total_idr,count(*) AS transactions
 FROM mimo.finance_transactions t LEFT JOIN mimo.finance_categories c ON c.id=t.category_id AND c.owner_id=$1
 WHERE t.owner_id=$1 AND t.status='posted' AND t.type<>'transfer' AND t.occurred_at>=$2 AND t.occurred_at<$3
 GROUP BY c.name,t.type ORDER BY total_idr DESC`, a.C.OwnerID, start, end)
	if err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"from": start.Format("2006-01-02"), "to_exclusive": end.Format("2006-01-02"), "expense_idr": expense, "income_idr": income, "transfers_idr": transfers, "net_cashflow_idr": income - expense, "categories": cats})
}
