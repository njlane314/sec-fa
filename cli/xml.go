package main

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strings"
)

type xmlNode struct {
	Name     xml.Name
	Attr     []xml.Attr
	Text     string
	Children []*xmlNode
}

func parseXMLTree(data []byte) (*xmlNode, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		switch strings.ToLower(strings.TrimSpace(charset)) {
		case "", "utf-8", "us-ascii", "ascii":
			return input, nil
		default:
			return nil, errors.New("unsupported XML charset: " + charset)
		}
	}
	var stack []*xmlNode
	var root *xmlNode
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &xmlNode{Name: t.Name, Attr: append([]xml.Attr(nil), t.Attr...)}
			if len(stack) > 0 {
				parent := stack[len(stack)-1]
				parent.Children = append(parent.Children, n)
			} else {
				root = n
			}
			stack = append(stack, n)
		case xml.EndElement:
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].Text += string(t)
			}
		}
	}
	if root == nil {
		return nil, errors.New("empty XML document")
	}
	return root, nil
}

func attr(n *xmlNode, local string) string {
	for _, a := range n.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}

func child(n *xmlNode, local string) *xmlNode {
	for _, c := range n.Children {
		if c.Name.Local == local {
			return c
		}
	}
	return nil
}

func descendants(n *xmlNode, local string, out *[]*xmlNode) {
	for _, c := range n.Children {
		if local == "" || c.Name.Local == local {
			*out = append(*out, c)
		}
		descendants(c, local, out)
	}
}

func firstDescendant(n *xmlNode, local string) *xmlNode {
	var out []*xmlNode
	descendants(n, local, &out)
	if len(out) == 0 {
		return nil
	}
	return out[0]
}

func text(n *xmlNode) string {
	if n == nil {
		return ""
	}
	var b strings.Builder
	var walk func(*xmlNode)
	walk = func(cur *xmlNode) {
		b.WriteString(cur.Text)
		for _, c := range cur.Children {
			walk(c)
		}
	}
	walk(n)
	return strings.TrimSpace(b.String())
}
