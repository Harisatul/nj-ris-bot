package app

import "testing"

func TestSplitInstallmentsPreservesTotal(t *testing.T) {
	got := splitInstallments(1000, 3)
	if len(got) != 3 || got[0] != 334 || got[1] != 333 || got[2] != 333 {
		t.Fatalf("unexpected split: %#v", got)
	}
	var total int64
	for _, n := range got {
		total += n
	}
	if total != 1000 {
		t.Fatalf("sum=%d", total)
	}
}

func TestValidateDebtTransactionShapes(t *testing.T) {
	valid := []moneyInput{
		{Type: "liability_purchase", AmountIDR: 600000, ToAccountID: "liability"},
		{Type: "liability_fee", AmountIDR: 10000, ToAccountID: "liability"},
		{Type: "debt_repayment", AmountIDR: 200000, FromAccountID: "asset", ToAccountID: "liability"},
	}
	for _, input := range valid {
		if why := validateMoney(input); why != "" {
			t.Errorf("%s: %s", input.Type, why)
		}
	}
	if why := validateMoney(moneyInput{Type: "debt_repayment", AmountIDR: 1, FromAccountID: "a", ToAccountID: "a"}); why == "" {
		t.Fatal("same account repayment should be rejected")
	}
}
