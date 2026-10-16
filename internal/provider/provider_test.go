package provider

import "testing"

func TestAccountKeyIsUniquePerScope(t *testing.T) {
	whole := Account{ID: "AAAA-1111", Name: "Prod"}
	web := Account{ID: "AAAA-1111", Name: "Prod", ResourceGroup: "rg-web"}
	data := Account{ID: "AAAA-1111", Name: "Prod", ResourceGroup: "rg-data"}
	other := Account{ID: "BBBB-2222", Name: "Dev", ResourceGroup: "rg-web"}

	keys := map[string]string{}
	for name, a := range map[string]Account{"whole": whole, "web": web, "data": data, "other": other} {
		if prev, dup := keys[a.Key()]; dup {
			t.Errorf("%s and %s share the key %q", name, prev, a.Key())
		}
		keys[a.Key()] = name
	}
}

func TestAccountKeyIgnoresCase(t *testing.T) {
	a := Account{ID: "AAAA-1111", ResourceGroup: "RG-Web"}
	b := Account{ID: "aaaa-1111", ResourceGroup: "rg-web"}
	if a.Key() != b.Key() {
		t.Errorf("%q != %q", a.Key(), b.Key())
	}
	if (Account{ID: "AAAA"}).Key() != (Account{ID: "aaaa"}).Key() {
		t.Error("subscription IDs must compare case-insensitively")
	}
}

func TestAccountLabel(t *testing.T) {
	if got := (Account{Name: "Office-Prod"}).Label(); got != "Office-Prod" {
		t.Errorf("whole subscription label = %q", got)
	}
	if got := (Account{Name: "Office-Prod", ResourceGroup: "rg-web"}).Label(); got != "Office-Prod / rg-web" {
		t.Errorf("resource group label = %q", got)
	}
}
