//go:build integration

// Copyright (c) 2026, WSO2 LLC. (https://www.wso2.com).
//
// WSO2 LLC. licenses this file to you under the Apache License,
// Version 2.0 (the "License"); you may not use this file except
// in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing,
// software distributed under the License is distributed on an
// "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
// KIND, either express or implied.  See the License for the
// specific language governing permissions and limitations
// under the License.

package tests

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"

	"github.com/wso2/agent-manager/agent-manager-service/db"
	dbmigrations "github.com/wso2/agent-manager/agent-manager-service/db_migrations"
)

// gatewayRolesTestTx opens a transaction and drops the functionality-type CHECK
// within it, so tests can seed pre-migration-037 values ('regular', 'ai') even
// though the live schema (already migrated to latest) only allows
// 'ingress'/'egress'/'both'. Rolling back at the end of the test undoes both the
// seeded rows and the constraint drop, so nothing persists and no other test
// (or a rerun of this one) ever observes leftover state.
func gatewayRolesTestTx(t *testing.T) *gorm.DB {
	t.Helper()
	gdb := db.DB(context.Background())
	tx := gdb.Begin()
	require.NoError(t, tx.Error)
	t.Cleanup(func() {
		require.NoError(t, tx.Rollback().Error)
	})
	require.NoError(t, tx.Exec(`ALTER TABLE gateways DROP CONSTRAINT IF EXISTS chk_gateway_functionality_type`).Error)
	return tx
}

// seedRoleGateway inserts a gateway row with an explicit (possibly pre-migration)
// functionality type, bypassing GORM's model defaults so the raw value is used verbatim.
func seedRoleGateway(t *testing.T, tx *gorm.DB, functionalityType string) uuid.UUID {
	t.Helper()
	id := uuid.New()
	name := "role-test-" + id.String()[:8]
	require.NoError(t, tx.Exec(
		`INSERT INTO gateways (uuid, name, display_name, vhost, gateway_functionality_type, ou_id)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		id, name, name, name+".gateway.local", functionalityType, "test-org",
	).Error)
	return id
}

// mapGatewayToEnvironment inserts a gateway_environment_mappings row.
func mapGatewayToEnvironment(t *testing.T, tx *gorm.DB, gatewayID, environmentID uuid.UUID) {
	t.Helper()
	require.NoError(t, tx.Exec(
		`INSERT INTO gateway_environment_mappings (gateway_uuid, environment_uuid) VALUES (?, ?)`,
		gatewayID, environmentID,
	).Error)
}

// runRoleBackfill replays migration 037's back-fill UPDATEs (regular->both,
// ai->egress) against the test transaction. It is not a call into the migration
// package because only the guard is exported (deliberately, per the migration's
// own doc comment) — the mapping is two one-line UPDATEs and is asserted at the
// value level here rather than by driving unexported migration internals.
func runRoleBackfill(t *testing.T, tx *gorm.DB) {
	t.Helper()
	require.NoError(t, tx.Exec(
		`UPDATE gateways SET gateway_functionality_type = 'both' WHERE gateway_functionality_type = 'regular'`,
	).Error)
	require.NoError(t, tx.Exec(
		`UPDATE gateways SET gateway_functionality_type = 'egress' WHERE gateway_functionality_type = 'ai'`,
	).Error)
}

// Case 1: regular -> both; ai -> egress.
func TestGatewayRolesMigration_BackfillMapping(t *testing.T) {
	tx := gatewayRolesTestTx(t)
	regularID := seedRoleGateway(t, tx, "regular")
	aiID := seedRoleGateway(t, tx, "ai")

	runRoleBackfill(t, tx)

	var regularType, aiType string
	require.NoError(t, tx.Raw(`SELECT gateway_functionality_type FROM gateways WHERE uuid = ?`, regularID).
		Scan(&regularType).Error)
	require.NoError(t, tx.Raw(`SELECT gateway_functionality_type FROM gateways WHERE uuid = ?`, aiID).
		Scan(&aiType).Error)

	assert.Equal(t, "both", regularType)
	assert.Equal(t, "egress", aiType)
}

// Case 2: the regular+ai pair from `make setup-ai-gateway` back-fills to both+egress
// and must upgrade cleanly — egress is uncapped, so this is not a conflict.
func TestGatewayRolesMigration_GuardPermitsBothAndEgress(t *testing.T) {
	tx := gatewayRolesTestTx(t)
	envID := uuid.New()
	regularID := seedRoleGateway(t, tx, "regular")
	aiID := seedRoleGateway(t, tx, "ai")
	mapGatewayToEnvironment(t, tx, regularID, envID)
	mapGatewayToEnvironment(t, tx, aiID, envID)

	runRoleBackfill(t, tx)

	assert.NoError(t, dbmigrations.AssertSingleIngressPerEnvironment(tx))
}

// Case 3: two 'regular' gateways mapped to the same environment both back-fill to
// 'both' and the guard must abort, naming both gateways and the remediation SQL.
func TestGatewayRolesMigration_GuardFiresOnDuplicateIngress(t *testing.T) {
	tx := gatewayRolesTestTx(t)
	envID := uuid.New()
	gw1 := seedRoleGateway(t, tx, "regular")
	gw2 := seedRoleGateway(t, tx, "regular")
	mapGatewayToEnvironment(t, tx, gw1, envID)
	mapGatewayToEnvironment(t, tx, gw2, envID)

	runRoleBackfill(t, tx)

	err := dbmigrations.AssertSingleIngressPerEnvironment(tx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), gw1.String())
	assert.Contains(t, err.Error(), gw2.String())
	assert.Contains(t, err.Error(), "SET gateway_functionality_type = 'ai'")
}

// Case 4: a soft-deleted gateway must not count toward the ingress cap — raw SQL
// bypasses GORM's soft-delete scope, so the guard filters deleted_at itself.
func TestGatewayRolesMigration_GuardIgnoresSoftDeleted(t *testing.T) {
	tx := gatewayRolesTestTx(t)
	envID := uuid.New()
	gw1 := seedRoleGateway(t, tx, "regular")
	gw2 := seedRoleGateway(t, tx, "regular")
	mapGatewayToEnvironment(t, tx, gw1, envID)
	mapGatewayToEnvironment(t, tx, gw2, envID)
	require.NoError(t, tx.Exec(`UPDATE gateways SET deleted_at = NOW() WHERE uuid = ?`, gw2).Error)

	runRoleBackfill(t, tx)

	assert.NoError(t, dbmigrations.AssertSingleIngressPerEnvironment(tx))
}

// Case 5: is_active is WebSocket liveness, not registration state, so the guard must
// still abort even when one of the conflicting gateways is currently inactive.
func TestGatewayRolesMigration_GuardIgnoresIsActive(t *testing.T) {
	tx := gatewayRolesTestTx(t)
	envID := uuid.New()
	gw1 := seedRoleGateway(t, tx, "regular")
	gw2 := seedRoleGateway(t, tx, "regular")
	mapGatewayToEnvironment(t, tx, gw1, envID)
	mapGatewayToEnvironment(t, tx, gw2, envID)
	require.NoError(t, tx.Exec(`UPDATE gateways SET is_active = false WHERE uuid = ?`, gw2).Error)

	runRoleBackfill(t, tx)

	err := dbmigrations.AssertSingleIngressPerEnvironment(tx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), gw1.String())
	assert.Contains(t, err.Error(), gw2.String())
}

// Case 6: post-migration-037 the DEFAULT is gone, so an INSERT that omits
// gateway_functionality_type must fail on NOT NULL, never reach the CHECK. This
// runs against the live (already fully migrated) schema rather than a relaxed
// transaction, since it is asserting the real, current column definition.
func TestGatewayRolesMigration_InsertWithoutRoleFailsNotNull(t *testing.T) {
	gdb := db.DB(context.Background())
	name := "role-test-notnull-" + uuid.New().String()[:8]
	t.Cleanup(func() {
		// Only matters if the insert unexpectedly succeeds; otherwise a no-op.
		gdb.Exec(`DELETE FROM gateways WHERE name = ?`, name)
	})

	err := gdb.Exec(
		`INSERT INTO gateways (name, display_name, vhost) VALUES (?, ?, ?)`,
		name, name, name+".gateway.local",
	).Error

	require.Error(t, err)
	var pgErr *pgconn.PgError
	require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError, got %T: %v", err, err)
	assert.Equal(t, "23502", pgErr.Code,
		"expected not_null_violation (23502), got code %s: %s", pgErr.Code, pgErr.Message)
	assert.Equal(t, "gateway_functionality_type", pgErr.ColumnName)
}
