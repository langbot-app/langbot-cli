package command

func hasHelpFlag(args []string) bool {
	for _, arg := range args {
		if arg == "--help" || arg == "-h" {
			return true
		}
		if arg == "--" {
			break
		}
	}
	return false
}
