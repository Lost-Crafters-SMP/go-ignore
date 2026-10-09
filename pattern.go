// SPDX-License-Identifier: Apache-2.0

package ignore

import (
	"fmt"
	"strings"
)

type opcode uint8

const (
	literal opcode = iota
	star
	crossStar
	directorySpan
	anyByte
	byteClass
)

type instruction struct {
	kind opcode
	text string
	set  [4]uint64
}

func compilePattern(pattern string, fold bool) ([]instruction, error) {
	var program []instruction
	var text strings.Builder
	flush := func() {
		if text.Len() != 0 {
			program = append(program, instruction{kind: literal, text: text.String()})
			text = strings.Builder{}
		}
	}
	for i := 0; i < len(pattern); {
		b := pattern[i]
		switch b {
		case '\\':
			if i+1 == len(pattern) {
				return nil, fmt.Errorf("trailing escape")
			}
			text.WriteByte(pattern[i+1])
			i += 2
		case '*':
			flush()
			end := i + 1
			for end < len(pattern) && pattern[end] == '*' {
				end++
			}
			separatorSize := 0
			if end < len(pattern) && pattern[end] == '/' {
				separatorSize = 1
			}
			if end+1 < len(pattern) && pattern[end:end+2] == "\\/" {
				separatorSize = 2
			}
			kind := star
			if end-i >= 2 && (i == 0 || pattern[i-1] == '/') && (end == len(pattern) || separatorSize != 0) {
				kind = crossStar
				if separatorSize == 1 {
					kind = directorySpan
					end += separatorSize
				}
			}
			program = append(program, instruction{kind: kind})
			i = end
		case '?':
			flush()
			program = append(program, instruction{kind: anyByte})
			i++
		case '[':
			flush()
			set, end, err := parseClass(pattern, i, fold)
			if err != nil {
				return nil, err
			}
			program = append(program, instruction{kind: byteClass, set: set})
			i = end
		default:
			text.WriteByte(b)
			i++
		}
	}
	flush()
	return program, nil
}

func addMember(set *[4]uint64, b byte, fold bool) {
	b = foldByte(b, fold)
	set[b/64] |= uint64(1) << (b % 64)
}

func parseClass(pattern string, start int, fold bool) ([4]uint64, int, error) {
	var set [4]uint64
	i := start + 1
	negative := false
	if i < len(pattern) && (pattern[i] == '!' || pattern[i] == '^') {
		negative = true
		i++
	}
	first := true
	for i < len(pattern) {
		if pattern[i] == ']' && !first {
			if negative {
				for k := range set {
					set[k] = ^set[k]
				}
			}
			set['/'/64] &^= uint64(1) << ('/' % 64)
			return set, i + 1, nil
		}
		first = false
		if strings.HasPrefix(pattern[i:], "[:") {
			end := strings.Index(pattern[i+2:], ":]")
			if end < 0 {
				return set, 0, fmt.Errorf("unclosed POSIX class")
			}
			name := pattern[i+2 : i+2+end]
			if !validClassName(name) {
				return set, 0, fmt.Errorf("unknown POSIX class %q", name)
			}
			for n := 0; n < 128; n++ {
				if posixMember(name, byte(n)) {
					addMember(&set, byte(n), fold)
				}
			}
			i += end + 4
			continue
		}
		b, next, err := classByte(pattern, i)
		if err != nil {
			return set, 0, err
		}
		i = next
		// The initial member remains literal even when a following range is empty.
		addMember(&set, b, fold)
		if i+1 < len(pattern) && pattern[i] == '-' && pattern[i+1] != ']' {
			last, after, err := classByte(pattern, i+1)
			if err != nil {
				return set, 0, err
			}
			for n := int(b); n <= int(last); n++ {
				addMember(&set, byte(n), fold)
			}
			i = after
		} else {
			addMember(&set, b, fold)
		}
	}
	return set, 0, fmt.Errorf("unclosed character class")
}

func classByte(pattern string, i int) (byte, int, error) {
	if pattern[i] == '\\' {
		if i+1 >= len(pattern) {
			return 0, 0, fmt.Errorf("trailing class escape")
		}
		return pattern[i+1], i + 2, nil
	}
	return pattern[i], i + 1, nil
}

func validClassName(name string) bool {
	switch name {
	case "alnum", "alpha", "blank", "cntrl", "digit", "graph", "lower", "print", "punct", "space", "upper", "xdigit":
		return true
	}
	return false
}

func posixMember(name string, b byte) bool {
	alpha := b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
	digit := b >= '0' && b <= '9'
	switch name {
	case "alnum":
		return alpha || digit
	case "alpha":
		return alpha
	case "blank":
		return b == ' ' || b == '\t'
	case "cntrl":
		return b < 32 || b == 127
	case "digit":
		return digit
	case "graph":
		return b > 32 && b < 127
	case "lower":
		return b >= 'a' && b <= 'z'
	case "print":
		return b >= 32 && b < 127
	case "punct":
		return b > 32 && b < 127 && !alpha && !digit
	case "space":
		return b == ' ' || b >= '\t' && b <= '\r'
	case "upper":
		return b >= 'A' && b <= 'Z'
	case "xdigit":
		return digit || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F'
	}
	return false
}

// runProgram uses two reachability rows and never recurses. A literal transition
// costs its run length; wildcard closure is a single forward pass per instruction.
// Time is O((compiled units + literal bytes)*candidate bytes), scratch O(candidate).
func runProgram(program []instruction, path string, slash, fold bool) bool {
	n := len(path)
	if slash {
		n++
	}
	// Literal-only rules need no reachability rows, including long source lines.
	if len(program) == 0 {
		return n == 0
	}
	if len(program) == 1 && program[0].kind == literal {
		text := program[0].text
		if len(text) != n {
			return false
		}
		if slash {
			return text[n-1] == '/' && equalBytes(text[:n-1], path, fold)
		}
		return equalBytes(text, path, fold)
	}
	var fixed [2][256]bool
	var a, b []bool
	if n < len(fixed[0]) {
		a = fixed[0][:n+1]
		b = fixed[1][:n+1]
	} else {
		a = make([]bool, n+1)
		b = make([]bool, n+1)
	}
	a[0] = true
	get := func(i int) byte {
		if i == len(path) {
			return '/'
		}
		return path[i]
	}
	for _, ins := range program {
		clear(b)
		switch ins.kind {
		case literal:
			for i := 0; i+len(ins.text) <= n; i++ {
				if !a[i] {
					continue
				}
				ok := true
				for j := range len(ins.text) {
					if foldByte(get(i+j), fold) != foldByte(ins.text[j], fold) {
						ok = false
						break
					}
				}
				if ok {
					b[i+len(ins.text)] = true
				}
			}
		case anyByte, byteClass:
			for i := 0; i < n; i++ {
				c := foldByte(get(i), fold)
				if a[i] && c != '/' && (ins.kind == anyByte || ins.set[c/64]&(uint64(1)<<(c%64)) != 0) {
					b[i+1] = true
				}
			}
		case star, crossStar:
			for i := 0; i <= n; i++ {
				if a[i] {
					b[i] = true
				}
				if i < n && b[i] && (ins.kind == crossStar || get(i) != '/') {
					b[i+1] = true
				}
			}
		case directorySpan:
			reachable := false
			for i := 0; i <= n; i++ {
				if a[i] {
					b[i] = true
					reachable = true
				}
				if i < n && reachable && get(i) == '/' {
					b[i+1] = true
				}
			}
		}
		a, b = b, a
	}
	return a[n]
}
