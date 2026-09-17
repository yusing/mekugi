package router

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/yusing/mekugi/capturer"
	"github.com/yusing/mekugi/internal/shellsyntax"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

const (
	nativeExecCommandToolName    = "exec_command"
	mekugiApplyExecMarker        = "// mekugi-proxy: apply translated patch\n"
	mekugiNativeApplyMarker      = "# mekugi-proxy: apply translated patch\n"
	mekugiNativeReportMarker     = "# mekugi-proxy: return mekugi report\n"
	mekugiNativeDiagnosticMarker = "# mekugi-proxy: return mekugi diagnostic "
)

type codeModeCarrierKind string

const (
	codeModeCarrierCustom   codeModeCarrierKind = "custom"
	codeModeCarrierFunction codeModeCarrierKind = "function"
)

type codeModeCarrierCatalog map[string]codeModeCarrierKind

// buildCodeModeCarrierCatalog builds a catalog of Code Mode carrier tools from the tool catalog.
func buildCodeModeCarrierCatalog(tools *responsesToolCatalog, registry *toolRegistry) (codeModeCarrierCatalog, error) {
	catalog := make(codeModeCarrierCatalog)
	add := func(tool *responsesToolDefinition) error {
		name := tool.Name
		if name == "" {
			return nil
		}
		if _, registered := registry.contribution(name); registered {
			return fmt.Errorf("responses request already defines registered tool %s", name)
		}
		if name == applyPatchToolName {
			return nil
		}
		var kind codeModeCarrierKind
		switch tool.Type {
		case string(codeModeCarrierCustom):
			kind = codeModeCarrierCustom
		case string(codeModeCarrierFunction):
			kind = codeModeCarrierFunction
		default:
			return nil
		}
		if _, exists := catalog[name]; exists {
			return fmt.Errorf("code mode carrier %q is defined more than once", name)
		}
		catalog[name] = kind
		return nil
	}

	if tools.top.present {
		if err := tools.top.err; err != nil {
			return nil, fmt.Errorf("decode Responses tools for carrier catalog: %w", err)
		}
		for _, tool := range tools.top.tools {
			if err := add(tool); err != nil {
				return nil, err
			}
		}
	}
	if tools.inputObjectsErr == nil {
		for _, group := range tools.additional {
			if !group.tools.present {
				return nil, errors.New("decode additional tools for carrier catalog: unexpected end of JSON input")
			}
			if err := group.tools.err; err != nil {
				return nil, fmt.Errorf("decode additional tools for carrier catalog: %w", err)
			}
			for _, additionalTool := range group.tools.tools {
				if additionalTool.Type != "namespace" {
					if err := add(additionalTool); err != nil {
						return nil, err
					}
					continue
				}
				if additionalTool.nested == nil {
					return nil, errors.New("decode namespaced tools for carrier catalog: unexpected end of JSON input")
				}
				if err := additionalTool.nested.err; err != nil {
					return nil, fmt.Errorf("decode namespaced tools for carrier catalog: %w", err)
				}
				for _, tool := range additionalTool.nested.tools {
					if err := add(tool); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return catalog, nil
}

func (catalog codeModeCarrierCatalog) require(name string, kind codeModeCarrierKind) error {
	if name == "" {
		return errors.New("translator returned an empty carrier name")
	}
	available, ok := catalog[name]
	if !ok {
		return fmt.Errorf("Code Mode carrier %q is unavailable", name)
	}
	if available != kind {
		return fmt.Errorf("Code Mode carrier %q has kind %q, not %q", name, available, kind)
	}
	return nil
}

func carrierItemType(kind codeModeCarrierKind) string {
	if kind == codeModeCarrierFunction {
		return "function_call"
	}
	return "custom_tool_call"
}

func carrierOutputItemType(kind codeModeCarrierKind) string {
	if kind == codeModeCarrierFunction {
		return "function_call_output"
	}
	return "custom_tool_call_output"
}

func carrierPayloadField(kind codeModeCarrierKind) string {
	if kind == codeModeCarrierFunction {
		return "arguments"
	}
	return "input"
}

func renderCarrierDoneEvent(payload []byte, kind codeModeCarrierKind, carrierPayload string) ([]byte, error) {
	var event map[string]json.RawMessage
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, errors.New("decode tool-call input completion event")
	}
	eventType := "response.custom_tool_call_input.done"
	if kind == codeModeCarrierFunction {
		eventType = "response.function_call_arguments.done"
	}
	event["type"] = mustMarshalJSON(eventType)
	delete(event, "input")
	delete(event, "arguments")
	event[carrierPayloadField(kind)] = mustMarshalJSON(carrierPayload)
	return marshalProtocolJSON(event)
}

func shellQuoteArgument(value string) string {
	quoted, err := syntax.Quote(value, syntax.LangBash)
	if err == nil {
		return quoted
	}
	// NUL cannot be represented in an argv value. Preserve the prior carrier
	// construction so the native executor remains the owner of that rejection.
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

// mekugiNativeCommand generates a native shell command for an mekugi history entry.
func mekugiNativeCommand(history mekugiHistory) string {
	switch {
	case history.TranslationError != "":
		return mekugiNativeDiagnosticMarker + strconv.Quote(history.TranslationError) +
			"\nprintf %s " + shellQuoteArgument(history.TranslationError)
	case history.Applied || history.AlreadySatisfied || history.Patch == "":
		return mekugiNativeReportMarker + "printf %s " + shellQuoteArgument(history.Report)
	default:
		return mekugiNativeApplyMarker +
			"mekugi_apply_output=$(printf %s " + shellQuoteArgument(history.Patch) + " | apply_patch; " +
			"mekugi_status=$?; printf x; exit \"$mekugi_status\")\n" +
			"mekugi_status=$?\n" +
			"mekugi_apply_output=${mekugi_apply_output%x}\n" +
			"if [ \"$mekugi_status\" -ne 0 ]; then printf %s " + shellQuoteArgument(changeNotice(history.ChangeID)) + " \"$mekugi_apply_output\"; exit \"$mekugi_status\"; fi\n" +
			"printf %s " + shellQuoteArgument(history.Report)
	}
}

func workerCommand(executable string, arguments []string) string {
	var command strings.Builder
	command.WriteString(shellQuoteArgument(executable))
	for _, argument := range arguments {
		command.WriteString(" " + shellQuoteArgument(argument))
	}
	return command.String()
}

func (registry *toolRegistry) directBashExecCommand(arguments []string) (string, bool) {
	if len(arguments) != 2 || arguments[0] != "bash" || arguments[1] == "" {
		return "", false
	}
	command := strings.TrimSuffix(arguments[1], "\n")
	command = strings.TrimSuffix(command, "\r")
	if command == "" || strings.ContainsAny(command, "\r\n") {
		return "", false
	}
	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(strings.NewReader(command), "")
	if err != nil || len(program.Stmts) != 1 {
		return "", false
	}
	statement := program.Stmts[0]
	call, ok := statement.Cmd.(*syntax.CallExpr)
	if !ok || len(call.Args) == 0 || statement.Semicolon.IsValid() || statement.Negated ||
		statement.Background || statement.Coprocess || statement.Disown {
		return "", false
	}
	staticCommand := true
	syntax.Walk(call.Args[0], func(node syntax.Node) bool {
		// Walk reports nil after visiting each node's children.
		if node == nil {
			return true
		}
		switch node.(type) {
		case *syntax.Word, *syntax.Lit, *syntax.SglQuoted, *syntax.DblQuoted:
			return true
		default:
			staticCommand = false
			return false
		}
	})
	commandName, err := expand.Literal(&expand.Config{}, call.Args[0])
	if err != nil || !staticCommand || commandName == "" || commandName == commentaryArgumentName || commandName == "hrun" || commandName == "hchanges" || commandName == "hread" || interp.IsBuiltin(commandName) {
		return "", false
	}
	if registry.commandRouting != nil && slices.Contains(registry.commandRouting.Commands, commandName) {
		// Optional executor-side routing needs the worker's actual PATH and cwd.
		return "", false
	}
	if contribution, exists := registry.contribution(commandName); exists &&
		contribution.PluginID == builtinToolsPluginID && !contribution.ModelVisible {
		return "", false
	}
	nestedCommand := false
	syntax.Walk(statement, func(node syntax.Node) bool {
		switch node.(type) {
		case *syntax.CmdSubst, *syntax.ProcSubst:
			nestedCommand = true
			return false
		default:
			return true
		}
	})
	if nestedCommand {
		return "", false
	}
	return command, true
}

// execCarrierPayload generates the payload for an exec carrier tool call.
func (registry *toolRegistry) execCarrierPayload(
	kind codeModeCarrierKind,
	contribution toolContribution,
	sourceInput string,
	arguments []string,
	template string,
	params map[string]json.RawMessage,
	resultMetadata map[string]json.RawMessage,
	callIDs ...string,
) (string, error) {
	command, err := registry.execCarrierCommand(contribution, sourceInput, arguments, template, 0, callIDs...)
	if err != nil {
		return "", err
	}
	if _, exists := params["cmd"]; exists {
		return "", errors.New("exec params must not contain cmd")
	}
	if login, exists := params["login"]; exists && !bytes.Equal(bytes.TrimSpace(login), []byte("false")) {
		return "", errors.New("exec params login must be false")
	}
	if kind == codeModeCarrierFunction && len(resultMetadata) != 0 {
		metadata := string(mustMarshalJSON(resultMetadata))
		command += "\nmekugi_status=$?\nprintf '\\n%s\\n' " + shellQuoteArgument(metadata) + "\nexit \"$mekugi_status\""
	}
	return renderExecCarrier(
		kind,
		execCommandArguments(command, params),
		contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell",
		resultMetadata,
	), nil
}

func (registry *toolRegistry) execCarrierCommand(
	contribution toolContribution,
	sourceInput string,
	arguments []string,
	template string,
	outputTokens int,
	callIDs ...string,
) (string, error) {
	if registry == nil {
		return "", errors.New("tool registry is unavailable")
	}
	builtinShell := contribution.PluginID == builtinToolsPluginID && contribution.Name == "shell"
	if !builtinShell {
		if _, ok := registry.wrapper(contribution.Name); !ok {
			return "", fmt.Errorf("%s worker is unavailable", contribution.Name)
		}
	}
	workerArguments := arguments
	if builtinShell && outputTokens > 0 && len(arguments) > 0 {
		parsed, err := shellsyntax.Parse(sourceInput)
		if err != nil {
			return "", err
		}
		params := maps.Clone(parsed.Params)
		if params == nil {
			params = make(map[string]any)
		}
		if value, ok := params["max_output_tokens"].(float64); ok && value >= 1 && value < float64(outputTokens) {
			outputTokens = int(value)
		}
		params["max_output_tokens"] = outputTokens
		source := "#!params=" + string(mustMarshalJSON(params)) + "\n"
		if parsed.CommandTemplate != "" {
			source += "#!cmd=" + parsed.CommandTemplate + "\n"
		}
		source += parsed.Body
		workerArguments = slices.Clone(arguments)
		workerArguments[len(workerArguments)-1] = source
	}
	directSelected := false
	command := workerCommand(contribution.Name, workerArguments)
	if builtinShell && template == "" && outputTokens == 0 {
		// Preserve direct calls without adding worker flags or environment.
		parsed, err := shellsyntax.Parse("#!bash\n" + sourceInput)
		if err == nil && parsed.CommandTemplate == "" && len(arguments) > 0 {
			directArguments := slices.Clone(arguments)
			directArguments[len(directArguments)-1] = parsed.Body
			if direct, ok := registry.directBashExecCommand(directArguments); ok {
				directSelected = true
				command = direct
			}
		}
	}
	callID := ""
	if builtinShell && len(callIDs) != 0 && capturer.ValidAXIdentity(callIDs[0]) {
		callID = callIDs[0]
	}
	if builtinShell && !directSelected && len(workerArguments) >= 2 {
		invocation := shellInvocation{CallID: callID}
		if len(callIDs) > 1 && (shellInterpreterName(arguments[0]) == "bash" || shellInterpreterName(arguments[0]) == "sh") {
			invocation.JournalToken = callIDs[1]
		}
		workerArguments = slices.Clone(workerArguments)
		last := len(workerArguments) - 1
		workerArguments[last] = invocation.source(workerArguments[last])
		command = workerCommand(contribution.Name, workerArguments)
	}
	if template != "" {
		if strings.Count(template, "{.}") != 1 {
			return "", errors.New("exec command template must contain exactly one {.} placeholder")
		}
		command = strings.Replace(template, "{.}", command, 1)
	}
	return command, nil
}

// execCommandArguments creates the arguments map for an exec_command call.
func execCommandArguments(command string, params map[string]json.RawMessage) map[string]json.RawMessage {
	argumentsObject := maps.Clone(params)
	if argumentsObject == nil {
		argumentsObject = make(map[string]json.RawMessage)
	}
	argumentsObject["cmd"] = mustMarshalJSON(command)
	if _, exists := argumentsObject["login"]; !exists {
		argumentsObject["login"] = mustMarshalJSON(false)
	}
	return argumentsObject
}

// renderExecCarrier renders an exec carrier payload from arguments and metadata.
func renderExecCarrier(
	kind codeModeCarrierKind,
	arguments map[string]json.RawMessage,
	forwardNativeResult bool,
	resultMetadata map[string]json.RawMessage,
) string {
	encodedArguments := string(mustMarshalJSON(arguments))
	if kind == codeModeCarrierFunction {
		return encodedArguments
	}
	resultOutput := "text(result.output);"
	if forwardNativeResult || len(resultMetadata) != 0 {
		resultOutput = "text(JSON.stringify(result));"
		if len(resultMetadata) != 0 {
			resultOutput = "text(JSON.stringify(Object.assign({}, result, " + string(mustMarshalJSON(resultMetadata)) + ")));"
		}
	}
	return "const result = await tools.exec_command(" + encodedArguments + ");\n" + resultOutput
}

func misuseWarningProjection(warning string) string {
	return "text(" + string(mustMarshalJSON(warning+"\n")) + ");\n"
}

func insertExecCommandWarning(input, warning string) (string, string, bool, error) {
	call := strings.Index(input, codeModeExecCallPrefix)
	if call < 0 {
		return input, "", false, errors.New("Code Mode input has no exec_command call")
	}
	projectionStart := -1
	for _, projection := range []string{
		codeModeOutputProjection,
		codeModeJSONProjection,
		codeModeMetadataProjection,
	} {
		if index := strings.Index(input[call:], "\n"+projection); index >= 0 {
			index += call + 1
			if projectionStart < 0 || index < projectionStart {
				projectionStart = index
			}
		}
	}
	if projectionStart < 0 {
		return input, "", false, errors.New("Code Mode input has no result projection")
	}
	warningInput := misuseWarningProjection(warning)
	if strings.Contains(input[call:projectionStart], warningInput) {
		return input, warningInput, false, nil
	}
	return input[:projectionStart] + warningInput + input[projectionStart:], warningInput, true, nil
}

func (h mekugiHistory) carrierInput() string {
	if h.PluginID != "" || h.CarrierKind != "" {
		return h.CarrierPayload
	}
	if h.TranslationError != "" {
		return "text(" + strconv.Quote(h.TranslationError) + ");"
	}
	if h.Applied || h.AlreadySatisfied {
		return "text(" + strconv.Quote(h.Report) + ");"
	}
	apply := "await tools.apply_patch(" + strconv.Quote(h.Patch) + ");\n"
	if h.ChangeID != "" {
		apply = "try {\n" + apply + "} catch (error) { text(" + strconv.Quote(changeNotice(h.ChangeID)) + "); throw error; }\n"
	}
	return mekugiApplyExecMarker + apply + "text(" + strconv.Quote(h.Report) + ");"
}

func (h mekugiHistory) effectiveCarrierKind() codeModeCarrierKind {
	if h.CarrierKind != "" {
		return h.CarrierKind
	}
	return codeModeCarrierCustom
}

const (
	nativeExecCommandWarning = "Warning: Use `functions.shell`"

	codeModeExecCallPrefix        = "const result = await tools.exec_command("
	codeModeOutputProjection      = "text(result.output);"
	codeModeJSONProjection        = "text(JSON.stringify(result));"
	codeModeMetadataProjection    = "text(JSON.stringify(Object.assign({}, result, "
	codeModeMetadataProjectionEnd = ")));"

	shellInterpreterNamePattern = `python(?:[0-9]+(?:\.[0-9]+)*)?|pypy[0-9]*|node(?:js)?|bun|` +
		`bash|dash|fish|ksh|mksh|sh|yash|zsh|perl|ruby|php|lua(?:jit)?|` +
		`r(?:script)?|psql|mysql|sqlite3|pwsh|powershell`
)

var (
	shellInterpreterPattern = regexp.MustCompile(`(?i)^(?:` + shellInterpreterNamePattern + `)$`)
	shellCommandFlagPattern = regexp.MustCompile(`^-[euilx]*c$`)
)

type shellWrapperMisuse struct {
	Kind            string
	Interpreter     string
	InterpreterArgs []string
	wrapper         string
}

// Only recognize known source options and known operand-free prefixes. Guessing
// through an unknown option can mistake its operand for a source option.
func shellInterpreterFlag(name, flag string) (source, harmless bool) {
	switch {
	case strings.HasPrefix(name, "python"), strings.HasPrefix(name, "pypy"):
		return flag == "-c", flag == "-I" || flag == "-u" || flag == "-B" || flag == "-E" || flag == "-s" || flag == "-S"
	case name == "node" || name == "nodejs" || name == "bun":
		return flag == "-e" || flag == "--eval", flag == "--input-type=module" || flag == "--input-type=commonjs" || flag == "--trace-warnings"
	case name == "bash" || name == "sh" || name == "dash" || name == "zsh" ||
		name == "ksh" || name == "mksh" || name == "yash":
		return shellCommandFlagPattern.MatchString(flag), flag == "-e" || flag == "-u" || flag == "-x" || flag == "-l" || flag == "-i"
	case name == "fish":
		return flag == "-c" || flag == "--command", false
	case name == "php":
		return flag == "-r", false
	case name == "psql":
		return flag == "-c" || flag == "--command", false
	case name == "mysql":
		return flag == "-e" || flag == "--execute", false
	case name == "perl" || name == "ruby" || name == "r" || name == "rscript" || strings.HasPrefix(name, "lua"):
		return flag == "-e", false
	default:
		return false, false
	}
}

// Inspect shell syntax, not raw source: comments, quoted examples, and other
// interpreters' source are not interpreter invocations.
func shellInterpreterWrapperMisuses(contribution toolContribution, input string) []shellWrapperMisuse {
	if contribution.PluginID != builtinToolsPluginID || contribution.Name != "shell" {
		return nil
	}
	parsed, err := shellsyntax.Parse(input)
	if err != nil || parsed.HasScript {
		return nil
	}
	variant := syntax.LangBash
	switch shellsyntax.InterpreterIdentity(parsed.Interpreter[0]) {
	case "bash":
	case "sh":
		variant = syntax.LangPOSIX
	default:
		return nil
	}
	program, err := syntax.NewParser(syntax.Variant(variant)).Parse(strings.NewReader(parsed.Body), "")
	if err != nil {
		return nil
	}

	var misuses []shellWrapperMisuse
	seenKinds := make(map[string]bool)
	syntax.Walk(program, func(node syntax.Node) bool {
		statement, ok := node.(*syntax.Stmt)
		if !ok {
			return true
		}
		call, ok := statement.Cmd.(*syntax.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		name, literal := shellCatLiteral(call.Args[0])
		args := call.Args[1:]
		if shellsyntax.InterpreterIdentity(name) == "env" {
			// Only a literal assignment prefix is unambiguous. Do not guess
			// how env options or dynamic words will select a command.
			for len(args) > 0 {
				value, static := shellCatLiteral(args[0])
				if !static || strings.HasPrefix(value, "-") {
					return true
				}
				args = args[1:]
				if !strings.Contains(value, "=") {
					name, literal = value, true
					break
				}
			}
		}
		name = shellsyntax.InterpreterIdentity(name)
		if !literal || !shellInterpreterPattern.MatchString(name) {
			return true
		}
		var flags []string
		stdin := false
		for _, word := range args {
			value, static := shellCatLiteral(word)
			if !static {
				return true
			}
			if value == "-" {
				stdin = true
				break
			}
			if !strings.HasPrefix(value, "-") || value == "--" || strings.ContainsAny(value, " \t\r\n") {
				return true
			}
			sourceFlag, harmlessFlag := shellInterpreterFlag(name, value)
			if sourceFlag {
				kind := value
				if !strings.HasPrefix(value, "--") && len(value) > 2 {
					kind = "-" + value[len(value)-1:]
					flags = append(flags, "-"+value[1:len(value)-1])
				}
				if !seenKinds[kind] {
					seenKinds[kind] = true
					misuses = append(misuses, shellWrapperMisuse{
						Kind: kind, Interpreter: name, InterpreterArgs: flags, wrapper: value,
					})
				}
				// A heredoc alongside -c/-e supplies program data, not source.
				return true
			}
			if !harmlessFlag {
				return true
			}
			flags = append(flags, value)
		}
		for _, redirect := range statement.Redirs {
			if (redirect.Op != syntax.Hdoc && redirect.Op != syntax.DashHdoc) ||
				(redirect.N != nil && redirect.N.Value != "0") || seenKinds["heredoc"] {
				continue
			}
			wrapper := "<<"
			if stdin {
				wrapper = "- <<"
			}
			seenKinds["heredoc"] = true
			misuses = append(misuses, shellWrapperMisuse{
				Kind: "heredoc", Interpreter: name, InterpreterArgs: flags, wrapper: wrapper,
			})
		}
		return true
	})
	return misuses
}

func shellInterpreterWrapperWarning(misuse shellWrapperMisuse) string {
	program := "the program"
	interpreter := strings.ToLower(misuse.Interpreter)
	switch {
	case strings.HasPrefix(interpreter, "python"), strings.HasPrefix(interpreter, "pypy"):
		program = "the Python program"
	case interpreter == "node", interpreter == "nodejs", interpreter == "bun":
		program = "the JavaScript program"
	}

	invocationArgs := misuse.InterpreterArgs
	if strings.HasPrefix(misuse.wrapper, "-") && !strings.HasPrefix(misuse.wrapper, "--") &&
		len(misuse.wrapper) > 2 && misuse.Kind != "heredoc" {
		invocationArgs = invocationArgs[:len(invocationArgs)-1]
	}
	invocation := strings.Join(append([]string{misuse.Interpreter}, invocationArgs...), " ")
	if invocation != "" {
		invocation += " "
	}
	invocation += misuse.wrapper

	if strings.EqualFold(misuse.Interpreter, "bash") {
		if misuse.Kind == "heredoc" {
			return fmt.Sprintf(
				"functions.shell: warning: remove the `%s...` heredoc wrapper and submit the Bash script body directly without a shebang",
				invocation,
			)
		}
		return fmt.Sprintf(
			"functions.shell: warning: remove the `%s` wrapper and submit the Bash script body directly without a shebang",
			invocation,
		)
	}

	shebang := strings.Join(append([]string{"#!" + misuse.Interpreter}, misuse.InterpreterArgs...), " ")
	if misuse.Kind == "heredoc" {
		return fmt.Sprintf(
			"functions.shell: warning: remove the `%s...` heredoc wrapper; start the script with `%s` and put %s directly in the body",
			invocation,
			shebang,
			program,
		)
	}
	return fmt.Sprintf(
		"functions.shell: warning: replace `%s ...` with `%s` on the first line and put %s directly in the body",
		invocation,
		shebang,
		program,
	)
}

func nativeExecCommandInput(input string) (string, string, bool, bool) {
	usage := inspectCodeModeRuntime(input)
	if !usage.execCommand {
		return input, "", false, false
	}
	// An empty warning projection with detected=true defers delivery to the
	// router-owned tool result, avoiding the program's local `text` binding.
	if usage.textShadowed {
		return input, "", false, true
	}
	warningInput := misuseWarningProjection(nativeExecCommandWarning)
	if usage.nativeWarningPresent {
		return input, warningInput, false, true
	}
	// Insert at a syntax-tree statement boundary, never a source substring that
	// might occur inside a quoted command. Keep canonical carrier classification.
	offset := usage.nativeWarningOffset
	return input[:offset] + warningInput + input[offset:], warningInput, true, true
}
