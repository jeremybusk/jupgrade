package upgrade

import (
	"crypto/sha1"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// Review reads captured bytes without requiring the original checkout to still
// exist. Apply/verify continue to use ReadPlan and its live inventory validation.
func ReadPlanForReview(path string) (Plan, error) {
	p, err := readPlanData(path)
	if err != nil {
		return p, err
	}
	return p, validateReviewPlan(p)
}

func validateReviewPlan(p Plan) error {
	if p.Schema < 1 || p.Schema > 5 || !filepath.IsAbs(p.Root) {
		return fmt.Errorf("unsupported review plan")
	}
	if err := p.Policy.Validate(); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, file := range p.Files {
		if seen[file.Path] {
			return fmt.Errorf("duplicate file %s", file.Path)
		}
		seen[file.Path] = true
		if p.Schema < 5 && file.Action != "" {
			return fmt.Errorf("file lifecycle requires schema 5")
		}
		if err := validateFileRecord(file, p.Inputs); err != nil {
			return err
		}
	}
	return nil
}

type diffLine struct {
	kind byte
	text string
}

func lines(content string) []string {
	if content == "" {
		return nil
	}
	parts := strings.SplitAfter(content, "\n")
	if parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

func lineDiff(before, after string) []diffLine {
	a, b := lines(before), lines(after)
	prefix, suffix := 0, 0
	for prefix < len(a) && prefix < len(b) && a[prefix] == b[prefix] {
		prefix++
	}
	for suffix < len(a)-prefix && suffix < len(b)-prefix && a[len(a)-suffix-1] == b[len(b)-suffix-1] {
		suffix++
	}
	operations := []diffLine{}
	for _, text := range a[:prefix] {
		operations = append(operations, diffLine{' ', text})
	}
	x, y := a[prefix:len(a)-suffix], b[prefix:len(b)-suffix]
	// Bound memory for large generated files. A replacement of the differing
	// middle remains an exact patch even when minimal LCS would be too expensive.
	if len(x) > 0 && len(y) > 0 && int64(len(x)+1)*int64(len(y)+1) <= 4000000 {
		width := len(y) + 1
		table := make([]uint32, (len(x)+1)*width)
		for i := len(x) - 1; i >= 0; i-- {
			for j := len(y) - 1; j >= 0; j-- {
				if x[i] == y[j] {
					table[i*width+j] = table[(i+1)*width+j+1] + 1
				} else {
					down, right := table[(i+1)*width+j], table[i*width+j+1]
					if down > right {
						table[i*width+j] = down
					} else {
						table[i*width+j] = right
					}
				}
			}
		}
		i, j := 0, 0
		for i < len(x) || j < len(y) {
			if i < len(x) && j < len(y) && x[i] == y[j] {
				operations = append(operations, diffLine{' ', x[i]})
				i++
				j++
			} else if j == len(y) || (i < len(x) && table[(i+1)*width+j] >= table[i*width+j+1]) {
				operations = append(operations, diffLine{'-', x[i]})
				i++
			} else {
				operations = append(operations, diffLine{'+', y[j]})
				j++
			}
		}
	} else {
		for _, text := range x {
			operations = append(operations, diffLine{'-', text})
		}
		for _, text := range y {
			operations = append(operations, diffLine{'+', text})
		}
	}
	for _, text := range a[len(a)-suffix:] {
		operations = append(operations, diffLine{' ', text})
	}
	return operations
}

func blobHash(content string) string {
	h := sha1.New()
	_, _ = fmt.Fprintf(h, "blob %d%c", len(content), 0)
	_, _ = h.Write([]byte(content))
	return fmt.Sprintf("%x", h.Sum(nil))
}
func gitMode(mode uint32) string {
	if mode&0111 != 0 {
		return "100755"
	}
	return "100644"
}

func WritePlanDiff(writer io.Writer, p Plan) error {
	if err := validateReviewPlan(p); err != nil {
		return err
	}
	var output strings.Builder
	for _, file := range p.Files {
		oldPath, newPath := strconv.Quote("a/"+filepath.ToSlash(file.Path)), strconv.Quote("b/"+filepath.ToSlash(file.Path))
		fmt.Fprintf(&output, "diff --git %s %s\n", oldPath, newPath)
		oldHash, newHash := blobHash(file.Before), blobHash(file.After)
		if file.Action == "create" {
			oldPath = "/dev/null"
			oldHash = strings.Repeat("0", 40)
			fmt.Fprintf(&output, "new file mode %s\n", gitMode(file.AfterMode))
		}
		if file.Action == "delete" {
			newPath = "/dev/null"
			newHash = strings.Repeat("0", 40)
			fmt.Fprintf(&output, "deleted file mode %s\n", gitMode(file.BeforeMode))
		}
		fmt.Fprintf(&output, "index %s..%s\n", oldHash, newHash)
		if file.Before == file.After {
			continue
		}
		fmt.Fprintf(&output, "--- %s\n+++ %s\n", oldPath, newPath)
		ops := lineDiff(file.Before, file.After)
		for i := 0; i < len(ops); {
			for i < len(ops) && ops[i].kind == ' ' {
				i++
			}
			if i == len(ops) {
				break
			}
			start := i - 3
			if start < 0 {
				start = 0
			}
			end := i + 1
			for {
				j := end
				for j < len(ops) && ops[j].kind == ' ' {
					j++
				}
				if j == len(ops) || j-end > 6 {
					end += 3
					if end > len(ops) {
						end = len(ops)
					}
					break
				}
				end = j + 1
			}
			oldStart, newStart, oldCount, newCount := 1, 1, 0, 0
			for _, line := range ops[:start] {
				if line.kind != '+' {
					oldStart++
				}
				if line.kind != '-' {
					newStart++
				}
			}
			for _, line := range ops[start:end] {
				if line.kind != '+' {
					oldCount++
				}
				if line.kind != '-' {
					newCount++
				}
			}
			if oldCount == 0 {
				oldStart--
			}
			if newCount == 0 {
				newStart--
			}
			fmt.Fprintf(&output, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount)
			for _, line := range ops[start:end] {
				fmt.Fprintf(&output, "%c%s", line.kind, line.text)
				if !strings.HasSuffix(line.text, "\n") {
					output.WriteString("\n\\ No newline at end of file\n")
				}
			}
			i = end
		}
	}
	_, err := io.WriteString(writer, output.String())
	return err
}
