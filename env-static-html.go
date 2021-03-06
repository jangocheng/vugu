package vugu

import (
	"bytes"
	"fmt"
	"io"

	"golang.org/x/net/html/atom"

	"golang.org/x/net/html"
)

var _ Env = (*StaticHTMLEnv)(nil) // assert type

// StaticHTMLEnv is an environment that renders to static HTML.  Can be used to implement "server side rendering"
// as well as during testing.
type StaticHTMLEnv struct {
	reg ComponentTypeMap
	// ComponentTypeMap map[string]ComponentType // TODO: probably make this it's own type and have a global instance where things can register
	rootInst *ComponentInst
	out      io.Writer
}

// NewStaticHTMLEnv returns a new instance of StaticHTMLEnv initialized properly.
// The out and rootInst are required, components may be nil.
func NewStaticHTMLEnv(out io.Writer, rootInst *ComponentInst, components ComponentTypeMap) *StaticHTMLEnv {
	if components == nil {
		components = make(ComponentTypeMap)
	}
	return &StaticHTMLEnv{
		reg:      components,
		rootInst: rootInst,
		out:      out,
	}
}

// RegisterComponentType sets a component type with its tag name.
func (e *StaticHTMLEnv) RegisterComponentType(tagName string, ct ComponentType) {
	e.reg[tagName] = ct
}

// Render performs redering to static HTML.  The logic used is similar to JSEnv.Render
// however it will discard DOM events and is less careful about managing component lifecycle:
// components are simply created when needed and discarded after.
func (e *StaticHTMLEnv) Render() error {

	c := e.rootInst
	out := e.out

	vdom, css, err := c.Type.BuildVDOM(c.Data)
	if err != nil {
		return err
	}

	// addCss := func(vcss *VGNode) {
	// 	css.AppendChild(vcss.FirstChild)
	// }

	// walk the vdom and handle components along the way;
	// since this is a static render, we can be pretty naive
	// about how and when we instanciate the components

	// compInstMap := make(map[*VGNode]*ComponentInst)
	err = vdom.Walk(func(vgn *VGNode) error {

		// must be element
		if vgn.Type != ElementNode {
			return nil
		}

		// element name must match a component
		ct, ok := e.reg[vgn.Data]
		if !ok {
			return nil
		}

		// copy props and merge in static attributes where they don't conflict
		props := vgn.Props.Clone()
		for _, a := range vgn.Attr {
			if _, ok := props[a.Key]; !ok {
				props[a.Key] = a.Val
			}
		}

		// just make a new instance each time - this is static html output
		compInst, err := New(ct, props)
		if err != nil {
			return err
		}

		cdom, ccss, err := ct.BuildVDOM(compInst.Data)
		if err != nil {
			return err
		}

		if ccss != nil && ccss.FirstChild != nil {
			css.AppendChild(ccss.FirstChild)
		}

		// make cdom replace vgn

		// point Parent on each child of cdom to vgn
		for cn := cdom.FirstChild; cn != nil; cn = cn.NextSibling {
			cn.Parent = vgn
		}
		// replace vgn with cdom but preserve vgn.Parent, vgn.PrevSibling, vgn.NextSibling
		*vgn, vgn.Parent, vgn.PrevSibling, vgn.NextSibling = *cdom, vgn.Parent, vgn.PrevSibling, vgn.NextSibling

		return nil
	})

	// The basic strategy is to build an equivalent html.Node tree from our vdom, expanding InnerHTML along
	// the way, and then tell the html package to write it out

	// output css
	if css != nil && css.FirstChild != nil {

		cssn := &html.Node{
			Type:     html.ElementNode,
			Data:     "style",
			DataAtom: atom.Style,
		}
		cssn.AppendChild(&html.Node{
			Type: html.TextNode,
			Data: css.FirstChild.Data,
		})

		err = html.Render(out, cssn)
		if err != nil {
			return err
		}
	}

	ptrMap := make(map[*VGNode]*html.Node)

	var conv func(*VGNode) (*html.Node, error)
	conv = func(vgn *VGNode) (*html.Node, error) {

		if vgn == nil {
			return nil, nil
		}

		// see if it's already in map, if so just return it
		if n := ptrMap[vgn]; n != nil {
			return n, nil
		}

		var err error
		n := &html.Node{}
		// assign this first thing, so that everything below when it recurses will just point to the same instance
		ptrMap[vgn] = n

		// for all node pointers we recursively call conv, which will convert them or just return the pointer if already done
		// Parent
		n.Parent, err = conv(vgn.Parent)
		if err != nil {
			return n, err
		}
		// FirstChild
		n.FirstChild, err = conv(vgn.FirstChild)
		if err != nil {
			return n, err
		}
		// LastChild
		n.LastChild, err = conv(vgn.LastChild)
		if err != nil {
			return n, err
		}
		// PrevSibling
		n.PrevSibling, err = conv(vgn.PrevSibling)
		if err != nil {
			return n, err
		}
		// NextSibling
		n.NextSibling, err = conv(vgn.NextSibling)
		if err != nil {
			return n, err
		}

		// copy the other type and attr info
		n.Type = html.NodeType(vgn.Type)
		n.DataAtom = atom.Atom(vgn.DataAtom)
		n.Data = vgn.Data
		n.Namespace = vgn.Namespace

		for _, vgnAttr := range vgn.Attr {
			n.Attr = append(n.Attr, html.Attribute{Namespace: vgnAttr.Namespace, Key: vgnAttr.Key, Val: vgnAttr.Val})
		}

		// for bound properties we fmt.Sprint and assign as attrs
		propKeys := vgn.Props.OrderedKeys()
	propAttrLoop:
		for _, k := range propKeys {
			for i := range n.Attr {
				if n.Attr[i].Key == k { // if attr is there, overwrite
					n.Attr[i].Val = fmt.Sprint(vgn.Props[k])
					continue propAttrLoop
				}
			}
			// attr not there, add
			n.Attr = append(n.Attr, html.Attribute{Key: k, Val: fmt.Sprint(vgn.Props[k])})
		}

		// parse and expand InnerHTML if present
		if vgn.InnerHTML != "" {

			innerNs, err := html.ParseFragment(bytes.NewReader([]byte(vgn.InnerHTML)), cruftBody)
			if err != nil {
				return nil, err
			}
			// FIXME: do we just append all of this, what about case where there is already something inside?
			for _, innerN := range innerNs {
				n.AppendChild(innerN)
			}

		}

		return n, nil
	}
	outn, err := conv(vdom)
	if err != nil {
		return err
	}
	// log.Printf("outn: %#v", outn)

	err = html.Render(out, outn)
	if err != nil {
		return err
	}

	return nil
}
