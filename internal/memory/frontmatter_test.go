package memory

import (
	"errors"
	"testing"
)

func TestRenderEntryFileParseFrontmatterRoundTrip(t *testing.T) {
	fm := Frontmatter{Name: "Op", Description: "the operator", Type: TypeUser}
	data, err := renderEntryFile(fm, "body text\nsecond line\n")
	if err != nil {
		t.Fatalf("renderEntryFile: %v", err)
	}
	gotFM, gotBody, err := parseFrontmatter(data)
	if err != nil {
		t.Fatalf("parseFrontmatter: %v", err)
	}
	if gotFM != fm {
		t.Fatalf("frontmatter round trip = %+v, want %+v", gotFM, fm)
	}
	if gotBody != "body text\nsecond line\n" {
		t.Fatalf("body round trip = %q", gotBody)
	}
}

func TestParseFrontmatterRejectsMissingDelimiter(t *testing.T) {
	_, _, err := parseFrontmatter([]byte("no frontmatter here\n"))
	if err == nil {
		t.Fatal("want error for missing frontmatter delimiter")
	}
}

func TestParseFrontmatterRejectsEmptyName(t *testing.T) {
	data := []byte("---\nname: \"\"\ndescription: d\ntype: user\n---\n\nbody\n")
	_, _, err := parseFrontmatter(data)
	if !errors.Is(err, ErrEmptyName) {
		t.Fatalf("err = %v, want ErrEmptyName", err)
	}
}

func TestParseFrontmatterRejectsInvalidType(t *testing.T) {
	data := []byte("---\nname: n\ndescription: d\ntype: bogus\n---\n\nbody\n")
	_, _, err := parseFrontmatter(data)
	if !errors.Is(err, ErrInvalidType) {
		t.Fatalf("err = %v, want ErrInvalidType", err)
	}
}

func TestTypeValid(t *testing.T) {
	valid := []Type{TypeUser, TypeFeedback, TypeProject, TypeReference}
	for _, ty := range valid {
		if !ty.Valid() {
			t.Errorf("%q.Valid() = false, want true", ty)
		}
	}
	if Type("nope").Valid() {
		t.Error("\"nope\".Valid() = true, want false")
	}
}
