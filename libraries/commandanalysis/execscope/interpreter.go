package execscope

// Interpreter names literal interpreter input. Source files are read only within SourceLimit.
type Interpreter struct {
	Identity  string
	Arguments []string
	Directory string
	Stdin     string
}

func execProgramSource(input execProviderInput) (string, string, []string, string) {
	read := readInterpreterFile
	if input.created {
		read = func(string) (string, string) { return input.createdSource, "" }
	}
	return readSource(Interpreter{Identity: input.identity, Arguments: input.args, Directory: input.cwd, Stdin: input.stdin}, read)
}
