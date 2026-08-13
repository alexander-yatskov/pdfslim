package optimize

import (
	"math"
	"strconv"
	"strings"
	"unicode"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/matrix"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

type imageBounds struct {
	width  int
	height int
}

func renderedImageBounds(ctx *model.Context, dpi int) map[int]imageBounds {
	result := map[int]imageBounds{}
	for pageNr := 1; pageNr <= ctx.PageCount; pageNr++ {
		page, _, inherited, err := ctx.PageDict(pageNr, false)
		if err != nil {
			continue
		}
		content, err := ctx.PageContent(page, pageNr)
		if err != nil {
			continue
		}
		resources := inherited.Resources
		if o, found := page.Find("Resources"); found {
			if d, err := ctx.DereferenceDict(o); err == nil && d != nil {
				resources = d
			}
		}
		walkImageContent(ctx, string(content), resources, matrix.IdentMatrix, dpi, result, map[int]bool{})
	}
	return result
}

func walkImageContent(ctx *model.Context, content string, resources types.Dict, ctm matrix.Matrix, dpi int, result map[int]imageBounds, activeForms map[int]bool) {
	tokens := contentTokens(content)
	stack := []matrix.Matrix{}
	operands := []string{}
	for i := 0; i < len(tokens); i++ {
		token := tokens[i]
		switch token {
		case "q":
			stack = append(stack, ctm)
			operands = operands[:0]
		case "Q":
			if len(stack) > 0 {
				ctm = stack[len(stack)-1]
				stack = stack[:len(stack)-1]
			}
			operands = operands[:0]
		case "cm":
			if len(operands) >= 6 {
				values := operands[len(operands)-6:]
				var n [6]float64
				ok := true
				for j := range values {
					n[j], ok = pdfNumber(values[j])
					if !ok {
						break
					}
				}
				if ok {
					m := matrix.Matrix{{n[0], n[1], 0}, {n[2], n[3], 0}, {n[4], n[5], 1}}
					ctm = m.Multiply(ctm)
				}
			}
			operands = operands[:0]
		case "Do":
			if len(operands) > 0 {
				name := strings.TrimPrefix(operands[len(operands)-1], "/")
				walkXObject(ctx, name, resources, ctm, dpi, result, activeForms)
			}
			operands = operands[:0]
		default:
			if isContentOperand(token) {
				operands = append(operands, token)
			} else {
				operands = operands[:0]
			}
		}
	}
}

func walkXObject(ctx *model.Context, name string, resources types.Dict, ctm matrix.Matrix, dpi int, result map[int]imageBounds, activeForms map[int]bool) {
	o, found := resources.Find("XObject")
	if !found {
		return
	}
	xObjects, err := ctx.DereferenceDict(o)
	if err != nil || xObjects == nil {
		return
	}
	o, found = xObjects.Find(name)
	if !found {
		return
	}
	objNr := 0
	if ref, ok := o.(types.IndirectRef); ok {
		objNr = ref.ObjectNumber.Value()
	}
	sd, _, err := ctx.DereferenceStreamDict(o)
	if err != nil || sd == nil {
		return
	}
	subtype := sd.Subtype()
	if subtype == nil {
		return
	}
	if *subtype == "Image" {
		if objNr == 0 {
			return
		}
		w := int(math.Ceil(math.Hypot(ctm[0][0], ctm[0][1]) * float64(dpi) / 72))
		h := int(math.Ceil(math.Hypot(ctm[1][0], ctm[1][1]) * float64(dpi) / 72))
		bounds := result[objNr]
		bounds.width, bounds.height = max(bounds.width, w), max(bounds.height, h)
		result[objNr] = bounds
		return
	}
	if *subtype != "Form" || objNr != 0 && activeForms[objNr] {
		return
	}
	if objNr != 0 {
		activeForms[objNr] = true
		defer delete(activeForms, objNr)
	}
	if len(sd.Content) == 0 {
		if err := sd.Decode(); err != nil {
			return
		}
	}
	formResources := resources
	if o, found := sd.Find("Resources"); found {
		if d, err := ctx.DereferenceDict(o); err == nil && d != nil {
			formResources = d
		}
	}
	formCTM := ctm
	if a := sd.ArrayEntry("Matrix"); len(a) == 6 {
		var n [6]float64
		ok := true
		for i := range a {
			value, err := ctx.DereferenceNumber(a[i])
			if err != nil {
				ok = false
				break
			}
			n[i] = value
		}
		if ok {
			m := matrix.Matrix{{n[0], n[1], 0}, {n[2], n[3], 0}, {n[4], n[5], 1}}
			formCTM = m.Multiply(ctm)
		}
	}
	walkImageContent(ctx, string(sd.Content), formResources, formCTM, dpi, result, activeForms)
}

func pdfNumber(token string) (float64, bool) {
	n, err := strconv.ParseFloat(token, 64)
	return n, err == nil
}

func isContentOperand(token string) bool {
	if strings.HasPrefix(token, "/") {
		return true
	}
	_, ok := pdfNumber(token)
	return ok
}

// contentTokens returns the tokens needed for graphics-state tracking. Strings,
// comments, dictionaries, arrays, and inline image payloads are skipped.
func contentTokens(content string) []string {
	var tokens []string
	for i := 0; i < len(content); {
		if unicode.IsSpace(rune(content[i])) {
			i++
			continue
		}
		switch content[i] {
		case '%':
			for i < len(content) && content[i] != '\n' && content[i] != '\r' {
				i++
			}
			continue
		case '(':
			i = skipLiteralString(content, i)
			continue
		case '<':
			if i+1 < len(content) && content[i+1] == '<' {
				i = skipBalanced(content, i, "<<", ">>")
			} else {
				i++
				for i < len(content) && content[i] != '>' {
					i++
				}
				i = min(i+1, len(content))
			}
			continue
		case '[':
			i = skipBalanced(content, i, "[", "]")
			continue
		}
		start := i
		for i < len(content) && !unicode.IsSpace(rune(content[i])) && !strings.ContainsRune("()<>[]{}/%", rune(content[i])) {
			i++
		}
		if content[start] == '/' {
			i++
			for i < len(content) && !unicode.IsSpace(rune(content[i])) && !strings.ContainsRune("()<>[]{}/%", rune(content[i])) {
				i++
			}
		}
		if i == start {
			i++
			continue
		}
		token := content[start:i]
		tokens = append(tokens, token)
		if token == "BI" {
			if end := strings.Index(content[i:], " EI"); end >= 0 {
				i += end + 3
			}
		}
	}
	return tokens
}

func skipLiteralString(s string, start int) int {
	depth := 1
	for i := start + 1; i < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] == '(' {
			depth++
		} else if s[i] == ')' {
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return len(s)
}

func skipBalanced(s string, start int, open, close string) int {
	depth := 0
	for i := start; i < len(s); {
		if strings.HasPrefix(s[i:], open) {
			depth++
			i += len(open)
			continue
		}
		if strings.HasPrefix(s[i:], close) {
			depth--
			i += len(close)
			if depth == 0 {
				return i
			}
			continue
		}
		i++
	}
	return len(s)
}
