package aria2

import (
	"reflect"
	"testing"
)

func TestValidateMetainfoRejectsTrailingAndReturnsStableHash(t *testing.T) {
	data := []byte("d4:infod6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	first, err := ValidateMetainfo(data)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ValidateMetainfo(data)
	if err != nil || first != second || len(first) != 40 {
		t.Fatalf("hash=%q second=%q err=%v", first, second, err)
	}
	if _, err := ValidateMetainfo(append(data, 'x')); err == nil {
		t.Fatal("trailing metainfo accepted")
	}
}

func TestMetainfoTotalLengthSupportsSingleAndMultiFileV1(t *testing.T) {
	single := []byte("d4:infod6:lengthi7e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if length, err := MetainfoTotalLength(single); err != nil || length != 7 {
		t.Fatalf("single-file length = %d, err=%v", length, err)
	}

	multi := []byte("d4:infod5:filesld6:lengthi2e4:pathl1:aeed6:lengthi3e4:pathl1:beee4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if length, err := MetainfoTotalLength(multi); err != nil || length != 5 {
		t.Fatalf("multi-file length = %d, err=%v", length, err)
	}
}

func TestMetainfoTotalLengthRejectsMissingAndAmbiguousV1Lengths(t *testing.T) {
	missing := []byte("d4:infod4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if _, err := MetainfoTotalLength(missing); err == nil {
		t.Fatal("metainfo without a v1 payload length was accepted")
	}

	ambiguous := []byte("d4:infod5:filesle6:lengthi1e4:name1:x12:piece lengthi1e6:pieces20:01234567890123456789ee")
	if _, err := MetainfoTotalLength(ambiguous); err == nil {
		t.Fatal("metainfo mixing single and multi-file layouts was accepted")
	}
}

func TestMetainfoFileLayoutSupportsSingleAndMultiFileV1(t *testing.T) {
	single := []byte("d4:infod6:lengthi7e4:name5:a.bin12:piece lengthi1e6:pieces20:01234567890123456789ee")
	gotSingle, err := MetainfoFileLayout(single)
	if err != nil {
		t.Fatal(err)
	}
	wantSingle := MetainfoLayout{Files: []MetainfoFile{{Path: []string{"a.bin"}, Length: 7}}}
	if !reflect.DeepEqual(gotSingle, wantSingle) {
		t.Fatalf("single-file layout = %#v, want %#v", gotSingle, wantSingle)
	}

	multi := []byte("d4:infod5:filesld6:lengthi2e4:pathl3:dir5:a.txteed6:lengthi3e4:pathl5:b.txteee4:name4:root12:piece lengthi1e6:pieces20:01234567890123456789ee")
	gotMulti, err := MetainfoFileLayout(multi)
	if err != nil {
		t.Fatal(err)
	}
	wantMulti := MetainfoLayout{MultiFile: true, Files: []MetainfoFile{
		{Path: []string{"dir", "a.txt"}, Length: 2},
		{Path: []string{"b.txt"}, Length: 3},
	}}
	if !reflect.DeepEqual(gotMulti, wantMulti) {
		t.Fatalf("multi-file layout = %#v, want %#v", gotMulti, wantMulti)
	}
}

func TestMetainfoFileLayoutPrefersUTF8Aliases(t *testing.T) {
	data := []byte("d4:infod5:filesld6:lengthi1e4:pathl1:ae10:path.utf-8l1:beee4:name1:x10:name.utf-81:y12:piece lengthi1e6:pieces20:01234567890123456789ee")
	layout, err := MetainfoFileLayout(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(layout.Files) != 1 || !reflect.DeepEqual(layout.Files[0].Path, []string{"b"}) {
		t.Fatalf("UTF-8 layout = %#v", layout)
	}
}
