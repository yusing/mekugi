package execscope

// Interpreter names literal interpreter input. Source files are read only within SourceLimit.
type Interpreter struct {
	Identity  string
	Arguments []string
	Directory string
	Stdin     string
}

func execProgramSource(input execProviderInput) (string, string, string) {
	if input.created {
		return input.createdSource, input.createdScript, ""
	}
	return ReadSource(Interpreter{Identity: input.identity, Arguments: input.args, Directory: input.cwd, Stdin: input.stdin})
}
