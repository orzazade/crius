package wakewatch

import "testing"

// Owner 10-23: a tab on a picked Max account is titled "<project> · <role> · <account>"; it is still the role's tab.
func TestATitleThatNamesItsAccountIsTheRolesTab(t *testing.T) {
	for title, want := range map[string]string{
		"quotes · p6 · max2": "quotes:p6", "quotes · p6": "quotes:p6", "quotes: lead": "quotes:lead",
		"quotes · p6 · max 2": "", "quotes · p6 · ": "",
	} {
		if who, _ := session("", title); who != want {
			t.Errorf("%q: %q, want %q", title, who, want)
		}
	}
}
