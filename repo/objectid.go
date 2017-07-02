package repo

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"fmt"
)

// ObjectID is an identifier of a repository object. Repository objects can be stored:
//
// 1. In a single storage block, this is the most common case for objects up to typically ~20MB.
// Storage blocks are encrypted with key specified in EncryptionKey.
//
// 2. In a series of storage blocks with an indirect block pointing at them (multiple indirections are allowed). This is used for larger files.
//
// 3. Inline as part of the ObjectID (typically for very small or empty files).
//
// 4. As sections of other objects (bundles).
//
// ObjectIDs have standard string representation (returned by UIString() and accepted as input to ParseObjectID()) suitable for using
// in user interfaces, such as command-line tools:
//
// Examples:
//
//   "B"                                        // empty object
//   "BcXVpY2sgYnJvd24gZm94Cg=="                // inline content "quick brown fox" (base64-encoded)
//   "D295754edeb35c17911b1fdf853f572fe"        // storage block
//   "I1,2c33acbcba3569f943d9e8aaea7817c5"      // level-1 indirection block
//   "I3,e18604fe53ee670558eb4234d2e55cb7"      // level-3 indirection block
//   "Daad048fd5721b43adaa353c407d23ff6.5617c50fb1d71b6f7a2c4c8bacacef1d2222eaa4b2245a3714686c658f8af3d9"
//                                              // encrypted storage block with 256-bit key
//   "I2,87381a8631dcc86256233437338e27c4.81cf86361dbc9b7905f12f6f6b80d7ec0edd487eeb339e1193805e3f58ef9505"
//                                              // encrypted level-2 indirection block with 256-bit key
//   "S30,50,D295754edeb35c17911b1fdf853f572fe" // section of "D295754edeb35c17911b1fdf853f572fe" between [30,80)
//
//
type ObjectID struct {
	StorageBlock  string
	EncryptionKey []byte
	Indirect      int32
	TextContent   string
	BinaryContent []byte
	Section       *ObjectIDSection
}

// MarshalJSON emits ObjectID in standard string format.
func (oid *ObjectID) MarshalJSON() ([]byte, error) {
	s := oid.String()
	return json.Marshal(&s)
}

// UnmarshalJSON unmarshals Object ID from a JSON string.
func (oid *ObjectID) UnmarshalJSON(b []byte) error {
	var s string
	err := json.Unmarshal(b, &s)
	if err != nil {
		return err
	}

	*oid, err = ParseObjectID(s)
	return err
}

// ObjectIDSection represents details about a section of a repository object.
type ObjectIDSection struct {
	Start  int64    `json:"start"`
	Length int64    `json:"len"`
	Base   ObjectID `json:"base"`
}

// HasObjectID exposes the identifier of an object.
type HasObjectID interface {
	ObjectID() ObjectID
}

// NullObjectID is the identifier of an null/empty object.
var NullObjectID ObjectID

const objectIDEncryptionInfoSeparator = "."

var (
	inlineContentEncoding = base64.RawURLEncoding
)

// String returns string representation of ObjectID that is suitable for displaying in the UI.
//
// Note that the object ID name often contains its encryption key, which is sensitive and can be quite long (~100 characters long).
func (oid ObjectID) String() string {
	if oid.StorageBlock != "" {
		var encryptionSuffix string

		if len(oid.EncryptionKey) > 0 {
			encryptionSuffix = "." + hex.EncodeToString(oid.EncryptionKey)
		}

		if oid.Indirect > 0 {
			return fmt.Sprintf("I%v,%v%v", oid.Indirect, oid.StorageBlock, encryptionSuffix)
		}

		return "D" + oid.StorageBlock + encryptionSuffix
	}

	if oid.BinaryContent != nil {
		return "B" + inlineContentEncoding.EncodeToString(oid.BinaryContent)
	}

	if len(oid.TextContent) > 0 {
		return "T" + oid.TextContent
	}

	if oid.Section != nil {
		return fmt.Sprintf("S%v,%v,%v", oid.Section.Start, oid.Section.Length, oid.Section.Base.String())
	}

	return "B"
}

// Validate validates the ObjectID structure.
func (oid *ObjectID) Validate() error {
	var c int
	if len(oid.StorageBlock) > 0 {
		c++
	}

	if len(oid.TextContent) > 0 {
		c++
	}

	if len(oid.BinaryContent) > 0 {
		c++
	}

	if oid.Section != nil {
		c++
		if err := oid.Section.Base.Validate(); err != nil {
			return fmt.Errorf("invalid section data in %+v", oid)
		}
	}

	if c > 1 {
		return fmt.Errorf("inconsistent block content: %+v", oid)
	}

	if oid.Indirect > 0 && len(oid.StorageBlock) == 0 {
		return fmt.Errorf("indirect object without storage block: %+v", oid)
	}

	if len(oid.EncryptionKey) > 0 && len(oid.StorageBlock) == 0 {
		return fmt.Errorf("encryption key without storage block: %+v", oid)

	}

	return nil
}

// InlineObjectID returns ObjectID containing the specified content.
func InlineObjectID(content []byte) ObjectID {
	if !utf8.Valid(content) {
		return ObjectID{
			BinaryContent: content,
		}
	}

	jsonLen := 0
	binaryLen := inlineContentEncoding.EncodedLen(len(content))

	for _, b := range content {
		if b < 32 && (b != 9 && b != 10 && b != 13) {
			return ObjectID{
				BinaryContent: content,
			}
		}

		if b == '\\' || b == '"' || b == 9 || b == 10 || b == 13 {
			jsonLen += 2
		} else if b < 128 {
			jsonLen++
		} else {
			jsonLen += 6
		}
	}

	if jsonLen < binaryLen {
		return ObjectID{
			TextContent: string(content),
		}
	}

	return ObjectID{
		BinaryContent: content,
	}

}

func isText(data []byte) bool {
	for _, b := range data {
		if b < 32 && (b != 9 && b != 10 && b != 13) {
			return false
		}
	}

	return true
}

// SectionObjectID returns new ObjectID representing a section of an object with a given base ID, start offset and length.
func SectionObjectID(start, length int64, baseID ObjectID) ObjectID {
	return ObjectID{
		Section: &ObjectIDSection{
			Base:   baseID,
			Start:  start,
			Length: length,
		},
	}
}

// parseNumberUntilComma parses a string of the form "{x},{remainder}" where x is a 64-bit number and remainder is arbitrary string.
// Returns the number and remainder.
func parseNumberUntilComma(s string) (int64, string, error) {
	comma := strings.IndexByte(s, ',')
	if comma < 0 {
		return 0, "", errors.New("missing comma")
	}

	num, err := strconv.ParseInt(s[0:comma], 10, 64)
	if err != nil {
		return 0, "", err
	}

	return num, s[comma+1:], nil
}

func parseSectionInfoString(s string) (int64, int64, ObjectID, error) {
	var start, length int64
	var err error

	start, s, err = parseNumberUntilComma(s[1:])
	if err != nil {
		return 0, -1, NullObjectID, err
	}

	length, s, err = parseNumberUntilComma(s)
	if err != nil {
		return 0, -1, NullObjectID, err
	}

	oid, err := ParseObjectID(s)
	if err != nil {
		return 0, -1, NullObjectID, err
	}

	return start, length, oid, nil
}

// ParseObjectID converts the specified string into ObjectID.
// The string format matches the output of UIString() method.
func ParseObjectID(s string) (ObjectID, error) {
	if len(s) >= 1 {
		chunkType := s[0]
		content := s[1:]

		switch chunkType {
		case 'S':
			if start, length, base, err := parseSectionInfoString(s); err == nil {
				return ObjectID{Section: &ObjectIDSection{
					Start:  start,
					Length: length,
					Base:   base,
				}}, nil
			}

		case 'B':
			if v, err := inlineContentEncoding.DecodeString(content); err == nil {
				return ObjectID{BinaryContent: v}, nil
			}

		case 'T':
			return ObjectID{TextContent: content}, nil

		case 'I', 'D':
			var indirectLevel int32
			if chunkType == 'I' {
				comma := strings.Index(content, ",")
				if comma < 0 {
					// malformed
					break
				}
				i, err := strconv.Atoi(content[0:comma])
				if err != nil {
					break
				}
				if i <= 0 {
					break
				}
				indirectLevel = int32(i)
				content = content[comma+1:]
				if content == "" {
					break
				}
			}

			firstSeparator := strings.Index(content, objectIDEncryptionInfoSeparator)
			lastSeparator := strings.LastIndex(content, objectIDEncryptionInfoSeparator)
			if firstSeparator == lastSeparator {
				if firstSeparator == -1 {
					// Found zero Separators in the ID - no encryption info.
					return ObjectID{StorageBlock: content, Indirect: indirectLevel}, nil
				}

				if firstSeparator > 0 {
					b, err := hex.DecodeString(content[firstSeparator+1:])
					if err == nil && len(b) > 0 {
						// Valid chunk ID with encryption info.
						return ObjectID{
							StorageBlock:  content[0:firstSeparator],
							EncryptionKey: b,
							Indirect:      indirectLevel,
						}, nil
					}
				}
			}
		}
	}

	return NullObjectID, fmt.Errorf("malformed object id: '%s'", s)
}
