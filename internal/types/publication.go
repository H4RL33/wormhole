package types

import (
	"errors"
	"fmt"
)

var ErrInvalidPublicationClassification = errors.New("types: invalid publication classification")

type PublicationClassification string

const (
	PublicationUnclassified PublicationClassification = "unclassified"
	PublicationLocalOnly    PublicationClassification = "local_only"
	PublicationPublicGit    PublicationClassification = "public_git"
	PublicationPrivateGit   PublicationClassification = "private_git"
)

func (classification PublicationClassification) Validate() error {
	switch classification {
	case PublicationUnclassified, PublicationLocalOnly, PublicationPublicGit, PublicationPrivateGit:
		return nil
	default:
		return fmt.Errorf("%w: %q", ErrInvalidPublicationClassification, classification)
	}
}
