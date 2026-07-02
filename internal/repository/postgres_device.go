package repository

import (
	"context"

	"github.com/christianselig/apollo-backend/internal/domain"
)

type postgresDeviceRepository struct {
	conn Connection
}

func NewPostgresDevice(conn Connection) domain.DeviceRepository {
	return &postgresDeviceRepository{conn: conn}
}

func (p *postgresDeviceRepository) fetch(ctx context.Context, query string, args ...interface{}) ([]domain.Device, error) {
	rows, err := p.conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var devs []domain.Device
	for rows.Next() {
		var dev domain.Device
		if err := rows.Scan(
			&dev.ID,
			&dev.APNSToken,
			&dev.Sandbox,
			&dev.Transport,
			&dev.TransportEndpoint,
		); err != nil {
			return nil, err
		}
		devs = append(devs, dev)
	}
	return devs, nil
}

func (p *postgresDeviceRepository) GetByID(ctx context.Context, id int64) (domain.Device, error) {
	query := `
		SELECT id, apns_token, sandbox, transport, transport_endpoint
		FROM devices
		WHERE id = $1`

	devs, err := p.fetch(ctx, query, id)

	if err != nil {
		return domain.Device{}, err
	}
	if len(devs) == 0 {
		return domain.Device{}, domain.ErrNotFound
	}
	return devs[0], nil
}

func (p *postgresDeviceRepository) GetByAPNSToken(ctx context.Context, token string) (domain.Device, error) {
	query := `
		SELECT id, apns_token, sandbox, transport, transport_endpoint
		FROM devices
		WHERE apns_token = $1`

	devs, err := p.fetch(ctx, query, token)

	if err != nil {
		return domain.Device{}, err
	}
	if len(devs) == 0 {
		return domain.Device{}, domain.ErrNotFound
	}
	return devs[0], nil
}

func (p *postgresDeviceRepository) GetByAccountID(ctx context.Context, id int64) ([]domain.Device, error) {
	query := `
		SELECT devices.id, apns_token, sandbox, transport, transport_endpoint
		FROM devices
		INNER JOIN devices_accounts ON devices.id = devices_accounts.device_id
		WHERE devices_accounts.account_id = $1`

	return p.fetch(ctx, query, id)
}

func (p *postgresDeviceRepository) GetInboxNotifiableByAccountID(ctx context.Context, id int64) ([]domain.Device, error) {
	query := `
		SELECT devices.id, apns_token, sandbox, transport, transport_endpoint
		FROM devices
		INNER JOIN devices_accounts ON devices.id = devices_accounts.device_id
		WHERE devices_accounts.account_id = $1 AND
		devices_accounts.inbox_notifiable = TRUE`

	return p.fetch(ctx, query, id)
}

func (p *postgresDeviceRepository) GetWatcherNotifiableByAccountID(ctx context.Context, id int64) ([]domain.Device, error) {
	query := `
		SELECT devices.id, apns_token, sandbox, transport, transport_endpoint
		FROM devices
		INNER JOIN devices_accounts ON devices.id = devices_accounts.device_id
		WHERE devices_accounts.account_id = $1 AND
		devices_accounts.watcher_notifiable = TRUE`

	return p.fetch(ctx, query, id)
}

// normalizeTransport keeps rows written by older tweak/client versions (which
// send no transport field) behaving exactly as before: plain APNs.
func normalizeTransport(dev *domain.Device) {
	if dev.Transport == "" {
		dev.Transport = domain.DeviceTransportAPNS
	}
}

func (p *postgresDeviceRepository) CreateOrUpdate(ctx context.Context, dev *domain.Device) error {
	normalizeTransport(dev)

	// The conflict-update path is how an existing device switches transports
	// (e.g. bark -> apns after a paid re-sign) or rotates its Bark push URL.
	query := `
		INSERT INTO devices (apns_token, sandbox, transport, transport_endpoint)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT(apns_token) DO
			UPDATE SET sandbox = $2, transport = $3, transport_endpoint = $4
		RETURNING id`

	return p.conn.QueryRow(
		ctx,
		query,
		dev.APNSToken,
		dev.Sandbox,
		dev.Transport,
		dev.TransportEndpoint,
	).Scan(&dev.ID)
}

func (p *postgresDeviceRepository) Create(ctx context.Context, dev *domain.Device) error {
	normalizeTransport(dev)

	if err := dev.Validate(); err != nil {
		return err
	}

	query := `
		INSERT INTO devices
			(apns_token, sandbox, transport, transport_endpoint)
		VALUES ($1, $2, $3, $4)
		RETURNING id`

	return p.conn.QueryRow(
		ctx,
		query,
		dev.APNSToken,
		dev.Sandbox,
		dev.Transport,
		dev.TransportEndpoint,
	).Scan(&dev.ID)
}

func (p *postgresDeviceRepository) Update(ctx context.Context, dev *domain.Device) error {
	normalizeTransport(dev)

	if err := dev.Validate(); err != nil {
		return err
	}

	query := `
		UPDATE devices
		SET sandbox = $2, transport = $3, transport_endpoint = $4
		WHERE id = $1`

	_, err := p.conn.Exec(ctx, query, dev.ID, dev.Sandbox, dev.Transport, dev.TransportEndpoint)
	return err
}

func (p *postgresDeviceRepository) Delete(ctx context.Context, token string) error {
	query := `DELETE FROM devices WHERE apns_token = $1`

	_, err := p.conn.Exec(ctx, query, token)
	return err
}

func (p *postgresDeviceRepository) SetNotifiable(ctx context.Context, dev *domain.Device, acct *domain.Account, inbox, watcher, global bool) error {
	query := `
		UPDATE devices_accounts
		SET
			inbox_notifiable = $1,
			watcher_notifiable = $2,
			global_mute = $3
		WHERE device_id = $4 AND account_id = $5`

	_, err := p.conn.Exec(ctx, query, inbox, watcher, global, dev.ID, acct.ID)
	return err

}

func (p *postgresDeviceRepository) GetNotifiable(ctx context.Context, dev *domain.Device, acct *domain.Account) (bool, bool, bool, error) {
	query := `
		SELECT inbox_notifiable, watcher_notifiable, global_mute
		FROM devices_accounts
		WHERE device_id = $1 AND account_id = $2`

	var inbox, watcher, global bool
	if err := p.conn.QueryRow(ctx, query, dev.ID, acct.ID).Scan(&inbox, &watcher, &global); err != nil {
		return false, false, false, domain.ErrNotFound
	}

	return inbox, watcher, global, nil
}
