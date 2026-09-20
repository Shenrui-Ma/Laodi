package laodi

import "strings"

// Only a direct Get-Content invocation is classified. Parsing is lexical and
// never evaluates PowerShell, expands variables, or opens the requested file.
// Native cmd, Git Bash and WSL invocations stay outside this grammar.
func hookPowerShellReadCommandPaths(command string) ([]string, bool) {
	words, ok := hookPowerShellWords(command)
	if !ok || len(words) < 2 || (!strings.EqualFold(words[0], "Get-Content") && !strings.EqualFold(words[0], `Microsoft.PowerShell.Management\Get-Content`)) {
		return nil, false
	}
	trimmed := strings.TrimLeft(command, " \t")
	if len(trimmed) <= len(words[0]) || !strings.EqualFold(trimmed[:len(words[0])], words[0]) || (trimmed[len(words[0])] != ' ' && trimmed[len(words[0])] != '\t') {
		return nil, false
	}
	var file string
	literal := false
	seen := make(map[string]bool)
	for i := 1; i < len(words); i++ {
		word := words[i]
		if strings.HasPrefix(word, "-") {
			option := strings.ToLower(word)
			if seen[option] {
				return nil, false
			}
			seen[option] = true
			switch option {
			case "-literalpath", "-path":
				i++
				if i >= len(words) || file != "" || strings.HasPrefix(words[i], "-") {
					return nil, false
				}
				file, literal = words[i], option == "-literalpath"
			case "-raw":
			case "-totalcount", "-tail":
				i++
				if i >= len(words) || words[i] == "" || strings.Trim(words[i], "0123456789") != "" {
					return nil, false
				}
			case "-encoding":
				i++
				if i >= len(words) {
					return nil, false
				}
				switch strings.ToLower(words[i]) {
				case "ascii", "unicode", "bigendianunicode", "utf8", "utf8bom", "utf8nobom", "utf32", "oem", "default":
				default:
					return nil, false
				}
			default:
				return nil, false
			}
		} else {
			if file != "" {
				return nil, false
			}
			file = word
		}
	}
	if file == "" || (seen["-raw"] && (seen["-totalcount"] || seen["-tail"])) || (seen["-totalcount"] && seen["-tail"]) {
		return nil, false
	}
	// Providers, ADS, wildcards and escaped paths are outside the verified file
	// grammar. A drive colon is allowed; UNC names remain lexical observations.
	colon := strings.Index(file, ":")
	if colon >= 0 && (colon != 1 || !isHookDriveLetter(file[0]) || len(file) < 3 || (file[2] != '\\' && file[2] != '/') || strings.Contains(file[2:], ":")) {
		return nil, false
	}
	if !literal && strings.ContainsAny(file, "*?[]") {
		return nil, false
	}
	return []string{file}, true
}

func isHookDriveLetter(b byte) bool { return (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z') }

// The quote rules deliberately accept less than PowerShell. In particular,
// adjacent quoted/unquoted fragments, backticks, expressions, arrays and
// expandable strings are not approximated. Single-quote doubling is literal.
func hookPowerShellWords(s string) ([]string, bool) {
	var words []string
	for i := 0; i < len(s); {
		for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
			i++
		}
		if i == len(s) {
			break
		}
		var word strings.Builder
		quote := byte(0)
		if s[i] == '\'' || s[i] == '"' {
			quote = s[i]
			i++
		}
		closed := quote == 0
		for i < len(s) {
			c := s[i]
			if c == 0 || c == '\r' || c == '\n' {
				return nil, false
			}
			if quote != 0 {
				if c == quote {
					if quote == '\'' && i+1 < len(s) && s[i+1] == '\'' {
						word.WriteByte('\'')
						i += 2
						continue
					}
					i++
					closed = true
					break
				}
				if quote == '"' && (c == '$' || c == '`') {
					return nil, false
				}
			} else {
				if c == ' ' || c == '\t' {
					break
				}
				if strings.ContainsRune("\"'`$;|&<>(){}*?[]#,@", rune(c)) {
					return nil, false
				}
			}
			word.WriteByte(c)
			i++
		}
		if !closed || (i < len(s) && s[i] != ' ' && s[i] != '\t') {
			return nil, false
		}
		if quote != 0 && strings.HasPrefix(word.String(), "-") {
			return nil, false
		}
		words = append(words, word.String())
	}
	return words, true
}
