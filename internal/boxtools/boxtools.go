// Package boxtools names the Crius tools that run on the Linux team machines (the Legion and the ThinkPad).
// One list: crius-install-box builds and copies exactly these, and crius-hook's TestLinuxToolsBuildFromTheRepo
// cross-builds exactly these, so the installer and the build test cannot name different tools.
package boxtools

// Tools are the commands under cmd/ that a box runs from its ~/.crius/bin.
var Tools = []string{"crius-hook", "bus-post", "crius-statusline", "playbook"}
