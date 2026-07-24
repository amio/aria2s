package aria2

import "testing"

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
