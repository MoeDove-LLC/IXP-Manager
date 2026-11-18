package main

import (
	"fmt"
	"strconv"

	"github.com/moedove/ixp-manager/internal/migrate"
)

func (c *converter) logos() error {
	seen := map[string]bool{}
	for _, r := range c.source.Tables["logos"] {
		member, err := c.consoleRef("logos", r, "customer_id", "cust", "members", false)
		if err != nil {
			return err
		}
		if seen[member] {
			return fmt.Errorf("logos: more than one current logo for member %s; review the source before conversion", member)
		}
		seen[member] = true
		get := func(key string) (string, error) { return c.consoleText("logos", r, key) }
		stored, err := get("stored_name")
		if err != nil {
			return err
		}
		original, err := get("original_name")
		if err != nil {
			return err
		}
		by, err := get("uploaded_by")
		if err != nil {
			return err
		}
		at, err := get("uploaded_at")
		if err != nil {
			return err
		}
		created, err := get("created_at")
		if err != nil {
			return err
		}
		if at == "" {
			at = created
		} else if created != "" && created != at {
			return fmt.Errorf("logos: conflicting upload timestamps for source %d", idOf(r))
		}
		source := migrate.LogoSource{ID: strconv.FormatInt(idOf(r), 10), MemberID: member, StoredName: stored, OriginalName: original, UploadedBy: by, UploadedAt: at}
		if err := migrate.ValidateLogoSource(source); err != nil {
			return err
		}
		c.result.LogoSources = append(c.result.LogoSources, source)
		// Pixel dimensions and type are derived from actual decoded bytes later.
		for _, key := range []string{"width", "height", "type", "updated_at"} {
			c.consoleUsed("logos", r, key)
		}
	}
	return nil
}

func buildLogoBundle(sources []migrate.LogoSource, root string) ([]byte, error) {
	if len(sources) == 0 {
		return nil, fmt.Errorf("logo bundle requires legacy logos records")
	}
	if len(sources) > migrate.LogoCountLimit {
		return nil, fmt.Errorf("logo count exceeds bundle limit")
	}
	items := []migrate.LogoItem{}
	total := 0
	var pixels int64
	for _, source := range sources {
		img, err := migrate.ReadLogoSource(root, source)
		if err != nil {
			return nil, fmt.Errorf("logo %s: %w", source.ID, err)
		}
		total += len(img.PNG)
		pixels += int64(img.Width) * int64(img.Height)
		if pixels > migrate.LogoPixelLimit {
			return nil, fmt.Errorf("logo bundle exceeds cumulative pixel limit")
		}
		if total > migrate.LogoDecodedLimit {
			return nil, fmt.Errorf("logo bundle exceeds cumulative PNG limit")
		}
		items = append(items, migrate.LogoItem{Source: source, PNG: img.PNG})
	}
	return migrate.EncodeLogoBundle(items)
}
