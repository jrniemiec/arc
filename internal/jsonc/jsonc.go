// Package jsonc provides JSON-with-comments parsing.
// It strips // line comments and /* */ block comments before delegating
// to encoding/json, so standard Go structs work without modification.
package jsonc

import (
	"encoding/json"
)

// Unmarshal strips comments from data and unmarshals the result into v.
func Unmarshal(data []byte, v any) error {
	return json.Unmarshal(strip(data), v)
}

// strip removes // line comments and /* */ block comments from JSON data,
// leaving string literals untouched.
func strip(src []byte) []byte {
	out := make([]byte, 0, len(src))
	i := 0
	for i < len(src) {
		// Inside a string — copy verbatim until closing quote.
		if src[i] == '"' {
			out = append(out, src[i])
			i++
			for i < len(src) {
				c := src[i]
				out = append(out, c)
				i++
				if c == '\\' && i < len(src) {
					// Escaped character — copy it and continue.
					out = append(out, src[i])
					i++
				} else if c == '"' {
					break
				}
			}
			continue
		}

		// Possible comment start.
		if i+1 < len(src) && src[i] == '/' {
			// Line comment: // … \n
			if src[i+1] == '/' {
				i += 2
				for i < len(src) && src[i] != '\n' {
					i++
				}
				continue
			}
			// Block comment: /* … */
			if src[i+1] == '*' {
				i += 2
				for i+1 < len(src) {
					if src[i] == '*' && src[i+1] == '/' {
						i += 2
						break
					}
					i++
				}
				continue
			}
		}

		out = append(out, src[i])
		i++
	}
	return out
}
