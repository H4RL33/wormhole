package types

import (
	"errors"
	"testing"
)

func TestPublicationClassificationValidate(t *testing.T) {
	for _, classification := range []PublicationClassification{
		PublicationUnclassified,
		PublicationLocalOnly,
		PublicationPublicGit,
		PublicationPrivateGit,
	} {
		if err := classification.Validate(); err != nil {
			t.Errorf("Validate(%q): %v", classification, err)
		}
	}

	for _, classification := range []PublicationClassification{
		"", "public", "PUBLIC_GIT", " public_git", "public_git\n",
	} {
		if err := classification.Validate(); !errors.Is(err, ErrInvalidPublicationClassification) {
			t.Errorf("Validate(%q) error = %v, want ErrInvalidPublicationClassification", classification, err)
		}
	}
}
