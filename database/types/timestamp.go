package types

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

type Timestamp struct {
	pgtype.Timestamp
}

func (t Timestamp) MarshalJSON() ([]byte, error) {
	if t.Valid {
		if t.InfinityModifier == pgtype.Infinity {
			return nil, errors.New("infinity timestamp")
		} else if t.InfinityModifier == pgtype.NegativeInfinity {
			return nil, errors.New("negative infinity timestamp")
		}
		b, err := json.Marshal(t.Timestamp.Time.Unix())
		return b, err
	} else {
		return []byte("null"), nil
	}
}

func (t *Timestamp) UnmarshalJSON(input []byte) error {
	if bytes.Equal(input, []byte("null")) {
		t.Valid = false
		t.Time = time.Unix(0, 0)
	} else {
		var i int64
		err := json.Unmarshal(input, &i)
		if err != nil {
			return fmt.Errorf("unmarshalling timestamp: %w", err)
		}
		t.Valid = true
		t.Time = time.Unix(i, 0).UTC()
		t.InfinityModifier = pgtype.Finite
	}

	return nil
}
