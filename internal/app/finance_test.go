package app

import "testing"

func TestValidateMoney(t *testing.T) {
	cases := []struct {
		name      string
		input     moneyInput
		wantValid bool
	}{
		{"expense", moneyInput{Type: "expense", AmountIDR: 28000, FromAccountID: "abc"}, true},
		{"income", moneyInput{Type: "income", AmountIDR: 1000, ToAccountID: "abc"}, true},
		{"transfer", moneyInput{Type: "transfer", AmountIDR: 500, FromAccountID: "a", ToAccountID: "b"}, true},
		{"negative", moneyInput{Type: "expense", AmountIDR: -1, FromAccountID: "a"}, false},
		{"transfer same account", moneyInput{Type: "transfer", AmountIDR: 500, FromAccountID: "a", ToAccountID: "a"}, false},
		{"expense missing source", moneyInput{Type: "expense", AmountIDR: 500}, false},
		{"transfer with category", moneyInput{Type: "transfer", AmountIDR: 500, FromAccountID: "a", ToAccountID: "b", CategoryID: "c"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			valid := validateMoney(c.input) == ""
			if valid != c.wantValid {
				t.Fatalf("valid=%v want %v", valid, c.wantValid)
			}
		})
	}
}
