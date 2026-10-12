package upgrade

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Byte ranges let us preserve comments, namespaces, whitespace, and all XML
// outside the exact scalar or insertion being changed.
type element struct {
	Name       string
	QName      string
	Start      int
	OpenEnd    int
	CloseStart int
	End        int
	Empty      bool
	Parent     *element
	Children   []*element
}

func parseXML(data []byte) (*element, error) {
	d := xml.NewDecoder(bytes.NewReader(data))
	var root, current *element
	for {
		start := int(d.InputOffset())
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("invalid POM XML: %w", err)
		}
		end := int(d.InputOffset())
		switch t := token.(type) {
		case xml.StartElement:
			raw := string(data[start:end])
			qname := strings.Fields(strings.TrimPrefix(raw, "<"))[0]
			qname = strings.TrimRight(qname, "/>")
			n := &element{Name: t.Name.Local, QName: qname, Start: start, OpenEnd: end, Parent: current, Empty: strings.HasSuffix(raw, "/>")}
			if current == nil {
				if root != nil {
					return nil, fmt.Errorf("multiple XML roots")
				}
				root = n
			} else {
				current.Children = append(current.Children, n)
			}
			current = n
		case xml.EndElement:
			if current == nil {
				return nil, fmt.Errorf("unexpected closing element")
			}
			current.CloseStart, current.End = start, end
			if current.Empty {
				current.CloseStart = current.OpenEnd
			}
			current = current.Parent
		case xml.CharData:
			if current == nil && strings.TrimSpace(string(t)) != "" {
				return nil, fmt.Errorf("text outside XML root")
			}
		}
	}
	if root == nil || root.Name != "project" {
		return nil, fmt.Errorf("expected a Maven project root")
	}
	return root, nil
}

func (n *element) child(name string) *element {
	for _, c := range n.Children {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func scalar(data []byte, n *element) string {
	if n == nil || n.Empty {
		return ""
	}
	var v string
	if err := xml.Unmarshal(data[n.Start:n.End], &v); err != nil {
		return ""
	}
	return strings.TrimSpace(v)
}

func textAt(data []byte, n *element, name string) string { return scalar(data, n.child(name)) }

func escaped(v string) string {
	var b bytes.Buffer
	_ = xml.EscapeText(&b, []byte(v))
	return b.String()
}

func tag(parent *element, name, value string) string {
	prefix := ""
	if i := strings.IndexByte(parent.QName, ':'); i >= 0 {
		prefix = parent.QName[:i+1]
	}
	return "<" + prefix + name + ">" + value + "</" + prefix + name + ">"
}

func replace(data []byte, start, end int, value string) []byte {
	out := make([]byte, 0, len(data)+len(value)-(end-start))
	out = append(out, data[:start]...)
	out = append(out, value...)
	return append(out, data[end:]...)
}

func setScalar(data []byte, parent *element, name, value string) ([]byte, error) {
	n := parent.child(name)
	if n == nil {
		return insert(data, parent, tag(parent, name, escaped(value))), nil
	}
	if len(n.Children) != 0 {
		return nil, fmt.Errorf("%s contains nested elements", name)
	}
	if n.Empty {
		return replace(data, n.Start, n.End, tag(parent, name, escaped(value))), nil
	}
	if scalar(data, n) == value {
		return data, nil
	}
	inner := string(data[n.OpenEnd:n.CloseStart])
	trim := strings.TrimSpace(inner)
	// Comments inside a scalar are unusual; refuse to silently remove them.
	if strings.Contains(inner, "<") {
		return nil, fmt.Errorf("%s contains markup; edit manually", name)
	}
	left := len(inner) - len(strings.TrimLeft(inner, " \r\n\t"))
	right := len(inner) - len(strings.TrimRight(inner, " \r\n\t"))
	if trim == "" {
		left, right = 0, 0
	}
	return replace(data, n.OpenEnd, n.CloseStart, inner[:left]+escaped(value)+inner[len(inner)-right:]), nil
}

func insert(data []byte, parent *element, markup string) []byte {
	if parent.Empty {
		return replace(data, parent.Start, parent.End, "<"+parent.QName+">"+markup+"</"+parent.QName+">")
	}
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	// Insert immediately after the opening tag; preserve all existing bytes.
	return replace(data, parent.OpenEnd, parent.OpenEnd, newline+"  "+markup)
}

func walk(n *element, fn func(*element)) {
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}
