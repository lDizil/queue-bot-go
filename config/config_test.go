package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestLoadPreservesLegacyDeploymentSettings(t *testing.T) {
	isolateConfiguration(t)
	writeConfiguration(t, "config.yaml", "total_slots_in_queue: 12\ndelay_update_queue: 2s\n")
	writeConfiguration(t, ".env", "TOTAL_SLOTS_IN_QUEUE=20\nAMOUNT_OF_BUTTONS_IN_ROW=4\nDELAY_QUEUE=500ms\nEXIRED_SES_TIME=8m\nWEEK1_DATE=2025-09-01\nWEEK1_TYPE=even\nBOT_CHAT_ID=-1001234567890\n")
	t.Setenv("TOTAL_SLOTS_IN_QUEUE", "25")
	t.Setenv("DELAY_QUEUE", "750ms")
	t.Setenv("BOT_TOKEN", "test-token")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Settings.TotalSlotsInQueue != 25 || cfg.Settings.AmountOfButtonsInRow != 4 {
		t.Fatalf("slot configuration = %+v, want environment slots 25 and legacy columns 4", cfg.Settings)
	}
	if cfg.Settings.DelayUpdateQueue != 750*time.Millisecond || cfg.Settings.TimeForExpiredEditSes != 8*time.Minute {
		t.Fatalf("legacy durations = %s and %s", cfg.Settings.DelayUpdateQueue, cfg.Settings.TimeForExpiredEditSes)
	}
	if cfg.Settings.Week1Date != "2025-09-01" || cfg.Settings.Week1Type != "even" {
		t.Fatalf("legacy semester configuration = %s %s", cfg.Settings.Week1Date, cfg.Settings.Week1Type)
	}
	if cfg.TelegramToken != "test-token" || cfg.ChatId != -1001234567890 || cfg.DeveloperUserID != 789915524 {
		t.Fatal("environment-only infrastructure values were not loaded")
	}
}

func TestLoadRejectsInfrastructureAndAccessSettingsInPublicYAML(t *testing.T) {
	for _, key := range []string{
		"bot_token", "bot_chat_id", "authorized_user_id", "developer_user_id",
		"db_user", "db_password", "db_name", "db_port", "db_host",
	} {
		t.Run(key, func(t *testing.T) {
			isolateConfiguration(t)
			const privateValue = "private-configuration-value"
			writeConfiguration(t, "config.yaml", key+": "+privateValue+"\n")
			_, err := Load()
			if err == nil {
				t.Fatalf("public config accepted protected field %s", key)
			}
			if strings.Contains(err.Error(), privateValue) {
				t.Fatal("public config validation exposed a protected value")
			}
		})
	}
}

func TestLoadKeepsDeveloperIdentityInEnvironment(t *testing.T) {
	isolateConfiguration(t)
	writeConfiguration(t, "config.yaml", "total_slots_in_queue: 24\n")
	writeConfiguration(t, ".env", "DEVELOPER_USER_ID=12345\nDB_PASSWORD=file-password\n")
	t.Setenv("DEVELOPER_USER_ID", "54321")
	t.Setenv("DB_PASSWORD", "process-password")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DeveloperUserID != 54321 || cfg.DBPass != "process-password" || cfg.Settings.TotalSlotsInQueue != 24 {
		t.Fatal("environment identity or infrastructure priority was lost")
	}
}

func TestLoadHasNoGlobalStateAcrossCalls(t *testing.T) {
	isolateConfiguration(t)
	writeConfiguration(t, "config.yaml", "total_slots_in_queue: 18\n")
	first, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	writeConfiguration(t, "config.yaml", "total_slots_in_queue: 24\n")
	second, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if first.Settings.TotalSlotsInQueue != 18 || second.Settings.TotalSlotsInQueue != 24 {
		t.Fatal("configuration reload reused previous values")
	}
}

func TestLoadDoesNotIncludeInvalidEnvironmentValuesInErrors(t *testing.T) {
	isolateConfiguration(t)
	const invalidValue = "private-value-that-must-not-appear-in-errors"
	t.Setenv("BOT_CHAT_ID", invalidValue)
	_, err := Load()
	if err == nil {
		t.Fatal("invalid chat ID accepted")
	}
	if strings.Contains(err.Error(), invalidValue) {
		t.Fatal("configuration error contains environment value")
	}
}

func isolateConfiguration(t *testing.T) {
	t.Helper()
	t.Chdir(t.TempDir())
	for _, names := range environmentKeys {
		for _, name := range names {
			original, existed := os.LookupEnv(name)
			if err := os.Unsetenv(name); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if existed {
					_ = os.Setenv(name, original)
					return
				}
				_ = os.Unsetenv(name)
			})
		}
	}
}

func writeConfiguration(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}
