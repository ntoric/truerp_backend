package utils

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestCompactSearchMatchesIgnoringWhitespace(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	type item struct {
		ID   uint
		Name string
	}
	if err := db.AutoMigrate(&item{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&[]item{{Name: "Apple Juice"}, {Name: "kitkat"}, {Name: "Mango\tPulp"}, {Name: "Banana"}})

	cases := map[string][]string{
		"Applejuice":  {"Apple Juice"},
		"apple juice": {"Apple Juice"},
		"kit kat":     {"kitkat"},
		"KIT  KAT":    {"kitkat"},
		"mangopulp":   {"Mango\tPulp"},
		"nan a":       {"Banana"},
		"xyz":         nil,
	}
	for search, want := range cases {
		var got []item
		if err := db.Where(SQLCompactLike("name"), CompactLikePattern(search)).Order("id").Find(&got).Error; err != nil {
			t.Fatalf("%q: %v", search, err)
		}
		if len(got) != len(want) {
			t.Fatalf("%q: got %d rows %+v, want %v", search, len(got), got, want)
		}
		for i := range want {
			if got[i].Name != want[i] {
				t.Errorf("%q: got %q, want %q", search, got[i].Name, want[i])
			}
		}
	}
	if CompactLikePattern("   ") != "" {
		t.Error("whitespace-only search should produce empty pattern")
	}
}
