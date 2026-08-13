package optimize

import (
	"math"
	"testing"

	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/matrix"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"
)

func TestWalkImageContentUsesCurrentTransformationMatrix(t *testing.T) {
	imageRef := *types.NewIndirectRef(7, 0)
	length := int64(0)
	imageStream := types.StreamDict{
		Dict:         types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Image")},
		StreamLength: &length,
	}
	xref := &model.XRefTable{Table: map[int]*model.XRefTableEntry{
		7: model.NewXRefTableEntryGen0(imageStream),
	}}
	resources := types.Dict{"XObject": types.Dict{"Im0": imageRef}}
	result := map[int]imageBounds{}

	walkImageContent(&model.Context{XRefTable: xref}, "q 144 0 0 72 10 20 cm /Im0 Do Q", resources, matrix.IdentMatrix, 100, result, map[int]bool{})

	if got := result[7]; got.width != 200 || got.height != 100 {
		t.Fatalf("rendered bounds = %+v, want 200x100", got)
	}
}

func TestWalkImageContentCombinesNestedMatrices(t *testing.T) {
	imageRef := *types.NewIndirectRef(7, 0)
	formRef := *types.NewIndirectRef(8, 0)
	length := int64(0)
	imageStream := types.StreamDict{Dict: types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Image")}, StreamLength: &length}
	formContent := []byte("2 0 0 3 0 0 cm /Im0 Do")
	formLength := int64(len(formContent))
	formStream := types.StreamDict{
		Dict:         types.Dict{"Type": types.Name("XObject"), "Subtype": types.Name("Form"), "Resources": types.Dict{"XObject": types.Dict{"Im0": imageRef}}},
		StreamLength: &formLength,
		Content:      formContent,
	}
	xref := &model.XRefTable{Table: map[int]*model.XRefTableEntry{
		7: model.NewXRefTableEntryGen0(imageStream),
		8: model.NewXRefTableEntryGen0(formStream),
	}}
	resources := types.Dict{"XObject": types.Dict{"Fm0": formRef}}
	result := map[int]imageBounds{}

	walkImageContent(&model.Context{XRefTable: xref}, "q 72 0 0 72 0 0 cm /Fm0 Do Q", resources, matrix.IdentMatrix, 72, result, map[int]bool{})

	got := result[7]
	if math.Abs(float64(got.width-144)) > 1 || math.Abs(float64(got.height-216)) > 1 {
		t.Fatalf("nested rendered bounds = %+v, want 144x216", got)
	}
}

func TestContentTokensIgnoreStringsAndComments(t *testing.T) {
	tokens := contentTokens("(fake /Bad Do) Tj % /No Do\n q 10 0 0 20 0 0 cm /Real Do Q")
	joined := ""
	for _, token := range tokens {
		joined += token + " "
	}
	if joined != "Tj q 10 0 0 20 0 0 cm /Real Do Q " {
		t.Fatalf("tokens = %q", joined)
	}
}
