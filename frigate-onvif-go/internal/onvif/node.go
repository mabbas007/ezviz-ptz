package onvif

import (
	"encoding/xml"
	"math"
	"strconv"
	"strings"
)

// node is a namespace-agnostic XML element tree. ONVIF clients differ in the
// prefixes they use, so lookups match local names only, like the Python
// bridge's ElementTree helpers.
type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []node     `xml:",any"`
}

func parseNode(data []byte) (*node, error) {
	var root node
	if err := xml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	return &root, nil
}

// find returns the first element (self included) with the given local name in
// depth-first order.
func (n *node) find(name string) *node {
	if n == nil {
		return nil
	}
	if n.XMLName.Local == name {
		return n
	}
	for i := range n.Children {
		if found := n.Children[i].find(name); found != nil {
			return found
		}
	}
	return nil
}

// child returns the first direct child with the given local name.
func (n *node) child(name string) *node {
	if n == nil {
		return nil
	}
	for i := range n.Children {
		if n.Children[i].XMLName.Local == name {
			return &n.Children[i]
		}
	}
	return nil
}

func (n *node) attr(name string) (string, bool) {
	if n == nil {
		return "", false
	}
	for _, attr := range n.Attrs {
		if attr.Name.Local == name {
			return attr.Value, true
		}
	}
	return "", false
}

// number reads a numeric attribute of the first descendant called name,
// returning 0 when the element or attribute is missing or malformed.
func number(n *node, name, attrName string) float64 {
	element := n.find(name)
	if element == nil {
		return 0
	}
	raw, ok := element.attr(attrName)
	if !ok {
		return 0
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0
	}
	return value
}
