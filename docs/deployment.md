# Deployment Guide

Complete guide to deploying s3gw on a Linux VM for production use with NFS and/or Samba file sharing.

## VM Requirements

### Minimum

- 1 vCPU
- 1 GB RAM
- Linux kernel 2.6.14+ (any modern distribution)
- FUSE support (`fuse3` package)
- Disk space for cache (separate from OS disk recommended)

### Recommended

- 2+ vCPU (FUSE context switching benefits from multiple cores)
- 2+ GB RAM (BoltDB uses mmap, benefits from available memory)
- Dedicated disk or partition for cache storage
- Place the VM in the same region as your Linode Object Storage for lowest latency

## Step-by-Step Deployment

### 1. Prepare the VM

```bash
# Update system
sudo apt update && sudo apt upgrade -y

# Install FUSE
sudo apt install -y fuse3

# Install NFS server (if using NFS)
sudo apt install -y nfs-kernel-server

# Install Samba (if using SMB)
sudo apt install -y samba
```

### 2. Prepare Disk for Cache

If using a dedicated disk for the cache (recommended):

```bash
# Format the disk (replace /dev/sdb with your actual device)
sudo mkfs.ext4 /dev/sdb

# Create mount point and mount
sudo mkdir -p /var/cache/s3gw
sudo mount /dev/sdb /var/cache/s3gw

# Add to fstab for persistence
echo '/dev/sdb /var/cache/s3gw ext4 defaults 0 2' | sudo tee -a /etc/fstab
```

### 3. Build and Install s3gw

```bash
# Clone the repository
git clone <repository-url>
cd s3gw

# Build
make build

# Install (binary, config, systemd unit)
sudo make install
```

### 4. Configure s3gw

```bash
# Copy example config
sudo cp /etc/s3gw/config.yaml.example /etc/s3gw/config.yaml

# Edit with your settings
sudo nano /etc/s3gw/config.yaml
```

Production configuration example:

```yaml
cache_dir: /var/cache/s3gw

# Set to ~80% of your dedicated cache disk.
# Example: 80 GB on a 100 GB disk
max_cache_size: 85899345920

# Default TTL. Higher = fewer S3 API calls. Lower = fresher data.
default_ttl: 10m

log_level: info

buckets:
  - name: company-documents
    region: us-east-1
    endpoint: https://us-east-1.linodeobjects.com
    access_key: LINODE_KEY_1
    secret_key: LINODE_SECRET_1
    mount_point: /mnt/s3/documents
    ttl: 30m
    sole_writer: true

  - name: media-assets
    region: us-east-1
    endpoint: https://us-east-1.linodeobjects.com
    access_key: LINODE_KEY_1
    secret_key: LINODE_SECRET_1
    mount_point: /mnt/s3/media
    ttl: 1h
    sole_writer: true

  - name: shared-workspace
    region: eu-central-1
    endpoint: https://eu-central-1.linodeobjects.com
    access_key: LINODE_KEY_2
    secret_key: LINODE_SECRET_2
    mount_point: /mnt/s3/shared
    ttl: 2m
    sole_writer: false
```

### 5. Create Mount Points

```bash
sudo mkdir -p /mnt/s3/documents /mnt/s3/media /mnt/s3/shared
```

### 6. Secure the Config File

The config file contains S3 credentials:

```bash
sudo chmod 600 /etc/s3gw/config.yaml
sudo chown root:root /etc/s3gw/config.yaml
```

### 7. Start s3gw

```bash
sudo systemctl enable --now s3gw

# Verify it started
sudo systemctl status s3gw

# Check mounts
mount | grep s3gw
```

Expected output:
```
s3gw:company-documents on /mnt/s3/documents type fuse.s3gw (rw,nosuid,nodev)
s3gw:media-assets on /mnt/s3/media type fuse.s3gw (rw,nosuid,nodev)
s3gw:shared-workspace on /mnt/s3/shared type fuse.s3gw (rw,nosuid,nodev)
```

### 8. Test Manually

```bash
# List files
ls /mnt/s3/documents/

# Write a test file
echo "hello" > /mnt/s3/documents/test.txt

# Read it back
cat /mnt/s3/documents/test.txt
```

## NFS Setup

### Configure NFS Exports

Edit `/etc/exports`:

```
/mnt/s3/documents  192.168.1.0/24(rw,sync,no_subtree_check,no_root_squash)
/mnt/s3/media      192.168.1.0/24(ro,sync,no_subtree_check)
/mnt/s3/shared     10.0.0.0/8(rw,sync,no_subtree_check,no_root_squash)
```

Apply and start:

```bash
sudo exportfs -ra
sudo systemctl enable --now nfs-kernel-server
```

### NFS Client Mount

On client machines:

```bash
sudo mount -t nfs <gateway-ip>:/mnt/s3/documents /mnt/documents
```

Or add to `/etc/fstab`:

```
<gateway-ip>:/mnt/s3/documents /mnt/documents nfs defaults 0 0
```

### NFS Performance Tuning

For large files, increase NFS read/write sizes:

```
/mnt/s3/documents  192.168.1.0/24(rw,sync,no_subtree_check,wsize=1048576,rsize=1048576)
```

## Samba Setup

### Configure Samba

Add to `/etc/samba/smb.conf`:

```ini
[documents]
   path = /mnt/s3/documents
   browsable = yes
   writable = yes
   valid users = @staff
   create mask = 0644
   directory mask = 0755

[media]
   path = /mnt/s3/media
   browsable = yes
   read only = yes
   guest ok = no
   valid users = @staff

[shared]
   path = /mnt/s3/shared
   browsable = yes
   writable = yes
   valid users = @staff
   create mask = 0644
   directory mask = 0755
```

Apply:

```bash
sudo smbpasswd -a <username>
sudo systemctl enable --now smbd
sudo systemctl enable --now nmbd
```

### Samba Client Access

- **Windows**: `\\<gateway-ip>\documents`
- **macOS**: Finder -> Go -> Connect to Server -> `smb://<gateway-ip>/documents`
- **Linux**: `sudo mount -t cifs //<gateway-ip>/documents /mnt/docs -o username=<user>`

## systemd Service Details

The included systemd unit (`/etc/systemd/system/s3gw.service`) provides:

- Automatic restart on failure (5-second delay)
- High file descriptor limit (65536)
- Filesystem protection (`ProtectSystem=strict`)
- Write access only to cache dir and mount points
- Private temp directory

### Customizing the Service

To override settings:

```bash
sudo systemctl edit s3gw
```

Example override to add mount paths:

```ini
[Service]
ReadWritePaths=/mnt/s3
ReadWritePaths=/var/cache/s3gw
ReadWritePaths=/mnt/custom-path
```

After editing:

```bash
sudo systemctl daemon-reload
sudo systemctl restart s3gw
```

## Firewall Rules

### NFS

```bash
# NFSv4 only needs port 2049
sudo ufw allow from 192.168.1.0/24 to any port 2049
```

### Samba

```bash
sudo ufw allow from 192.168.1.0/24 to any port 445
sudo ufw allow from 192.168.1.0/24 to any port 139
```

## Backup and Recovery

### What to Back Up

1. `/etc/s3gw/config.yaml` - Configuration (contains credentials)
2. That's it. The cache is reconstructable from S3.

### Recovery Procedure

1. Install s3gw on the new VM
2. Restore `/etc/s3gw/config.yaml`
3. Start the service
4. The cache will rebuild automatically as files are accessed

## Upgrading

```bash
# Stop the service
sudo systemctl stop s3gw

# Build new version
git pull
make build

# Install
sudo make install

# Start
sudo systemctl start s3gw
```

The cache data persists across upgrades. If the cache metadata format changes between versions, delete the cache DB:

```bash
sudo rm /var/cache/s3gw/cache.db
```

It will be recreated on startup.
