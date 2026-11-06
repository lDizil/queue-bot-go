package db

import (
	"encoding/json"
	"testing"
	"time"

	"queuebot/settings"
)

func TestLoadLegacySettingsDefaultsOnlyMissingModerationBlockDuration(t *testing.T) {
	repo, _ := newChatTestRepository(t, 8)
	original := settings.Defaults()
	original.TotalSlotsInQueue = 24
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var legacy map[string]json.RawMessage
	if err := json.Unmarshal(data, &legacy); err != nil {
		t.Fatal(err)
	}
	delete(legacy, "moderation_block_duration")
	for _, scenario := range []string{"legacy", "explicit zero", "missing required setting"} {
		t.Run(scenario, func(t *testing.T) {
			if scenario == "explicit zero" {
				legacy["moderation_block_duration"] = json.RawMessage("0")
			}
			if scenario == "missing required setting" {
				delete(legacy, "moderation_block_duration")
				delete(legacy, "total_slots_in_queue")
			}
			data, err := json.Marshal(legacy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := repo.pool.Exec(t.Context(), "INSERT INTO runtime_settings (id, value) VALUES (true, $1) ON CONFLICT (id) DO UPDATE SET value = EXCLUDED.value", data); err != nil {
				t.Fatal(err)
			}
			values, found, err := repo.LoadSettings(t.Context())
			if err != nil || !found {
				t.Fatalf("load persisted settings: found=%t err=%v", found, err)
			}
			if scenario == "legacy" {
				if values != original || values.ModerationBlockDuration != 3*time.Minute {
					t.Fatalf("legacy settings changed: %+v", values)
				}
				return
			}
			if err := values.Validate(); err == nil {
				t.Fatalf("invalid persisted settings were silently repaired: %+v", values)
			}
		})
	}
}
