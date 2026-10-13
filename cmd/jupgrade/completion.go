package main

import (
	"flag"
	"fmt"
	"strings"

	"jupgrade/internal/upgrade"
)

func completionValues(name string) string {
	switch name {
	case "java":
		return "17 21 25"
	case "build":
		return "auto maven gradle"
	case "preset":
		return "build common"
	case "format":
		return "unified summary json"
	case "recipe":
		names := []string{}
		for _, recipe := range upgrade.RecipeCatalog() {
			names = append(names, recipe.Name)
		}
		return strings.Join(names, " ")
	}
	return ""
}

func completionScript(shell string) (string, error) {
	if shell != "bash" && shell != "zsh" && shell != "fish" {
		return "", fmt.Errorf("completion shell must be bash, zsh or fish")
	}
	var script strings.Builder
	if shell == "fish" {
		script.WriteString("complete -c jupgrade -f\n")
		fmt.Fprintf(&script, "complete -c jupgrade -n '__fish_use_subcommand' -a '%s'\n", strings.Join(commandNames, " "))
		for _, name := range commandNames {
			command, _ := configureCommand(name)
			command.flags.VisitAll(func(f *flag.Flag) {
				fmt.Fprintf(&script, "complete -c jupgrade -n '__fish_seen_subcommand_from %s' -l %s", name, f.Name)
				if boolean, ok := f.Value.(interface{ IsBoolFlag() bool }); !ok || !boolean.IsBoolFlag() {
					script.WriteString(" -r")
				}
				if values := completionValues(f.Name); values != "" {
					fmt.Fprintf(&script, " -a '%s'", values)
				}
				switch f.Name {
				case "repo", "out", "plan", "model", "manifest", "policy", "java-home", "maven-repo", "maven-settings", "gradle-command", "gradle-user-home":
					script.WriteString(" -F")
				}
				script.WriteByte('\n')
			})
		}
		return script.String(), nil
	}
	if shell == "bash" {
		script.WriteString(`_jupgrade_complete() {
  local jupgrade_command="${COMP_WORDS[1]}" jupgrade_current="${COMP_WORDS[COMP_CWORD]}" jupgrade_previous="${COMP_WORDS[COMP_CWORD-1]}" jupgrade_options
  if (( COMP_CWORD == 1 )); then
`)
		fmt.Fprintf(&script, "    COMPREPLY=($(compgen -W '%s' -- \"$jupgrade_current\")); return\n  fi\n", strings.Join(commandNames, " "))
		script.WriteString("  case \"$jupgrade_previous\" in\n")
		for _, name := range []string{"java", "build", "preset", "format", "recipe"} {
			fmt.Fprintf(&script, "    --%s) COMPREPLY=($(compgen -W '%s' -- \"$jupgrade_current\")); return ;;\n", name, completionValues(name))
		}
		script.WriteString("  esac\n  case \"$jupgrade_command\" in\n")
	} else {
		fmt.Fprintf(&script, "#compdef jupgrade\n_jupgrade() {\n  local jupgrade_command=$words[2] jupgrade_options\n  if (( CURRENT == 2 )); then compadd -- %s; return; fi\n  case \"$jupgrade_command\" in\n", strings.Join(commandNames, " "))
	}
	for _, name := range commandNames {
		command, _ := configureCommand(name)
		options := []string{}
		command.flags.VisitAll(func(f *flag.Flag) { options = append(options, "--"+f.Name) })
		options = append(options, "--help")
		fmt.Fprintf(&script, "    %s) jupgrade_options='%s' ;;\n", name, strings.Join(options, " "))
	}
	if shell == "bash" {
		script.WriteString(`  esac
  if [[ "$jupgrade_current" == -* ]]; then
    COMPREPLY=($(compgen -W "$jupgrade_options" -- "$jupgrade_current"))
  else
    COMPREPLY=()
    while IFS= read -r jupgrade_match; do COMPREPLY+=("$jupgrade_match"); done < <(compgen -f -- "$jupgrade_current")
  fi
}
complete -F _jupgrade_complete jupgrade
`)
	} else {
		script.WriteString("  esac\n  if [[ $words[CURRENT] == -* ]]; then compadd -- ${(z)jupgrade_options}; else _files; fi\n}\ncompdef _jupgrade jupgrade\n")
	}
	return script.String(), nil
}
