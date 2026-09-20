package denylist

import "testing"

func TestPolicy(t *testing.T) {
	p, err := New([]string{"pkg:pypi/My_Package@1.0", "pkg:npm/@scope/name@2.0.0", "pkg:npm/%40scope/name@2.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"pkg:pypi/my-package@1.0", "pkg:npm/%40scope/name@2.0.0"} {
		if !p.Denied(value) {
			t.Errorf("not denied: %s", value)
		}
	}
	for _, value := range []string{"pkg:pypi/my-package@1.1", "pkg:pypi/my-package", "pkg:npm/other@2.0.0", "invalid"} {
		if p.Denied(value) {
			t.Errorf("unexpected denial: %s", value)
		}
	}
	versions := p.Versions("pkg:pypi/my-package")
	delete(versions, "1.0")
	if !p.Denied("pkg:pypi/my-package@1.0") {
		t.Fatal("caller mutated policy")
	}
	var empty *Policy
	if empty.Denied("pkg:npm/example@1") || empty.Versions("pkg:npm/example") != nil {
		t.Fatal("nil policy must allow everything")
	}
}

func TestInvalidPolicy(t *testing.T) {
	for _, value := range []string{"", "not-a-purl", "pkg:npm/foo", "pkg:npm/foo@*", "pkg:npm/foo@>=1", "pkg:npm/foo@1?arch=x86", "pkg:npm/foo@1#src"} {
		t.Run(value, func(t *testing.T) {
			if _, err := New([]string{value}); err == nil {
				t.Fatal("invalid denylist accepted")
			}
		})
	}
}
