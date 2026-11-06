package main

import (
	"strings"
	"testing"
)

// Review r2 of 34a1ff7: each P1 was a command the guard let through with no ask or deny.

// P1-1, P1-2: az takes an option's last copy in command-line order, and every alias and unambiguous prefix of a long
// option is that option. The guard read the first name it was given (--org before --organization, --target-branch
// before -t), and a prefix of an option it read only by its full name was another option.
func TestAzOptionLastCopyInOrderWinsOverAliasesAndPrefixes(t *testing.T) {
	dir := criusDir(t, map[string]string{"5": "refs/heads/integration/96365-b1"})
	for _, cmd := range []string{
		"az repos pr update --id 5 --status completed --org https://dev.azure.com/Appxite --organization https://dev.azure.com/Other",
		"az repos pr update --id 5 --status completed --org https://dev.azure.com/Appxite --organiz https://dev.azure.com/Other",
		"az repos pr update --id 5 --status completed --organization https://dev.azure.com/Other --org https://dev.azure.com/Appxite --orga https://dev.azure.com/Other",
	} {
		if d, why := crius(t, criusSID, dir, cmd); d != "deny" || !strings.Contains(why, "Appxite") {
			t.Errorf("Crius mode: want deny naming Appxite, got %s %q\n  %s", d, why, cmd)
		}
	}
	// the last copy is Appxite's: the call goes on
	if d, why := crius(t, criusSID, dir, "az repos pr update --id 5 --status completed --org https://dev.azure.com/Other --organization https://dev.azure.com/Appxite"); d != "" {
		t.Errorf("Crius mode: want allow, got %s %q", d, why)
	}
	const pr = "az repos pr create --title x --source-branch feature/96365-x --target-branch integration/96365-b1"
	for _, cmd := range []string{
		pr + " -t develop" + allowOrg,
		pr + " --target develop" + allowOrg,
		pr + " --target-b=develop" + allowOrg,
		pr + " -s main" + allowOrg,
		pr + " --sour main" + allowOrg,
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "owner's click") || !strings.Contains(why, "Not on the allow list") {
			t.Errorf("lead: want an ask from the allow list, got %q\n  %s", why, cmd)
		}
	}
	leadCheck(t, dir, "az repos pr create --title x -s main -t develop --source-branch feature/96365-x --target-b integration/96365-b1"+allowOrg, "")
	// az rest: -m and --method, -u, --uri and --url are one option each
	for _, cmd := range []string{
		"az rest --method get -m patch --url https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9",
		"az rest --url https://example.com --uri https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9 -m patch",
		"az rest --meth get --method patch -u https://dev.azure.com/Appxite/P/_apis/git/repositories/R/pullrequests/9",
	} {
		if why := check(bash(cmd, dir)); !strings.Contains(why, "az rest PATCH") {
			t.Errorf("want an ask for az rest PATCH, got %q\n  %s", why, cmd)
		}
	}
}
