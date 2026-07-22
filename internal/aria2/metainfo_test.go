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
