#!/bin/bash

# <UDF name="bucket1_name" Label="Bucket 1 - Name" example="my-bucket" />
# <UDF name="bucket1_region" Label="Bucket 1 - Region" oneOf="us-east-1,us-southeast-1,us-ord-1,us-iad-1,eu-central-1,ap-south-1,se-sto-1,us-mia-1,id-cgk-1,fr-par-1,in-maa-1,jp-osa-1,it-mil-1,us-lax-1,gb-lon-1,au-mel-1,br-gru-1,nl-ams-1,es-mad-1,sg-sin-1" />
# <UDF name="bucket1_access_key" Label="Bucket 1 - Access Key" example="Your Linode Object Storage access key" />
# <UDF name="bucket1_secret_key" Label="Bucket 1 - Secret Key" example="Your Linode Object Storage secret key" />
# <UDF name="bucket1_ttl" Label="Bucket 1 - Cache TTL" default="5m" example="e.g. 5m, 1h, 30s" />
# <UDF name="bucket1_sole_writer" Label="Bucket 1 - Sole Writer Mode" oneOf="true,false" default="true" />

# <UDF name="bucket2_name" Label="Bucket 2 - Name (leave empty to skip)" default="" example="Optional second bucket" />
# <UDF name="bucket2_region" Label="Bucket 2 - Region" oneOf="us-east-1,us-southeast-1,us-ord-1,us-iad-1,eu-central-1,ap-south-1,se-sto-1,us-mia-1,id-cgk-1,fr-par-1,in-maa-1,jp-osa-1,it-mil-1,us-lax-1,gb-lon-1,au-mel-1,br-gru-1,nl-ams-1,es-mad-1,sg-sin-1" default="us-east-1" />
# <UDF name="bucket2_access_key" Label="Bucket 2 - Access Key" default="" />
# <UDF name="bucket2_secret_key" Label="Bucket 2 - Secret Key" default="" />
# <UDF name="bucket2_ttl" Label="Bucket 2 - Cache TTL" default="5m" />
# <UDF name="bucket2_sole_writer" Label="Bucket 2 - Sole Writer Mode" oneOf="true,false" default="true" />

# <UDF name="bucket3_name" Label="Bucket 3 - Name (leave empty to skip)" default="" example="Optional third bucket" />
# <UDF name="bucket3_region" Label="Bucket 3 - Region" oneOf="us-east-1,us-southeast-1,us-ord-1,us-iad-1,eu-central-1,ap-south-1,se-sto-1,us-mia-1,id-cgk-1,fr-par-1,in-maa-1,jp-osa-1,it-mil-1,us-lax-1,gb-lon-1,au-mel-1,br-gru-1,nl-ams-1,es-mad-1,sg-sin-1" default="us-east-1" />
# <UDF name="bucket3_access_key" Label="Bucket 3 - Access Key" default="" />
# <UDF name="bucket3_secret_key" Label="Bucket 3 - Secret Key" default="" />
# <UDF name="bucket3_ttl" Label="Bucket 3 - Cache TTL" default="5m" />
# <UDF name="bucket3_sole_writer" Label="Bucket 3 - Sole Writer Mode" oneOf="true,false" default="true" />

# <UDF name="cache_size_gb" Label="Cache Size (GB)" default="10" example="Max disk space for cached S3 objects" />
# <UDF name="default_ttl" Label="Default Cache TTL" default="5m" example="Fallback TTL for buckets without a specific TTL" />
# <UDF name="log_level" Label="Log Level" oneOf="debug,info,warn,error" default="info" />

# <UDF name="sharing_protocol" Label="File Sharing Protocol" oneOf="nfs,samba,both" />
# <UDF name="nfs_allowed_network" Label="NFS - Allowed Network CIDR" default="10.0.0.0/8" example="Network allowed to mount NFS shares" />
# <UDF name="samba_user" Label="Samba - Username" default="" example="Required if Samba or Both is selected" />
# <UDF name="samba_password" Label="Samba - Password" default="" example="Required if Samba or Both is selected" />

set -euo pipefail

LOGFILE="/var/log/s3gw-stackscript.log"
exec > >(tee -a "$LOGFILE") 2>&1

echo "============================================="
echo " s3gw StackScript - $(date)"
echo "============================================="

GO_VERSION="1.22.5"
REPO_URL="https://github.com/slepix/linode-object-gateway"
CACHE_DIR="/var/cache/s3gw"
CONFIG_DIR="/etc/s3gw"
CONFIG_FILE="${CONFIG_DIR}/config.yaml"
MOUNT_BASE="/mnt/s3"

trim() {
    local val="$1"
    val="${val#"${val%%[![:space:]]*}"}"
    val="${val%"${val##*[![:space:]]}"}"
    echo "$val"
}

BUCKET1_NAME="$(trim "$BUCKET1_NAME")"
BUCKET1_REGION="$(trim "$BUCKET1_REGION")"
BUCKET1_ACCESS_KEY="$(trim "$BUCKET1_ACCESS_KEY")"
BUCKET1_SECRET_KEY="$(trim "$BUCKET1_SECRET_KEY")"
BUCKET1_TTL="$(trim "$BUCKET1_TTL")"
BUCKET1_SOLE_WRITER="$(trim "$BUCKET1_SOLE_WRITER")"

BUCKET2_NAME="$(trim "$BUCKET2_NAME")"
BUCKET2_REGION="$(trim "$BUCKET2_REGION")"
BUCKET2_ACCESS_KEY="$(trim "$BUCKET2_ACCESS_KEY")"
BUCKET2_SECRET_KEY="$(trim "$BUCKET2_SECRET_KEY")"
BUCKET2_TTL="$(trim "$BUCKET2_TTL")"
BUCKET2_SOLE_WRITER="$(trim "$BUCKET2_SOLE_WRITER")"

BUCKET3_NAME="$(trim "$BUCKET3_NAME")"
BUCKET3_REGION="$(trim "$BUCKET3_REGION")"
BUCKET3_ACCESS_KEY="$(trim "$BUCKET3_ACCESS_KEY")"
BUCKET3_SECRET_KEY="$(trim "$BUCKET3_SECRET_KEY")"
BUCKET3_TTL="$(trim "$BUCKET3_TTL")"
BUCKET3_SOLE_WRITER="$(trim "$BUCKET3_SOLE_WRITER")"

CACHE_SIZE_GB="$(trim "$CACHE_SIZE_GB")"
DEFAULT_TTL="$(trim "$DEFAULT_TTL")"
LOG_LEVEL="$(trim "$LOG_LEVEL")"
SHARING_PROTOCOL="$(trim "$SHARING_PROTOCOL")"
NFS_ALLOWED_NETWORK="$(trim "$NFS_ALLOWED_NETWORK")"
SAMBA_USER="$(trim "$SAMBA_USER")"
SAMBA_PASSWORD="$(trim "$SAMBA_PASSWORD")"

validate_inputs() {
    local errors=0

    if [ -z "$BUCKET1_NAME" ]; then
        echo "ERROR: Bucket 1 name is required."
        errors=$((errors + 1))
    fi
    if [ -z "$BUCKET1_ACCESS_KEY" ]; then
        echo "ERROR: Bucket 1 access key is required."
        errors=$((errors + 1))
    fi
    if [ -z "$BUCKET1_SECRET_KEY" ]; then
        echo "ERROR: Bucket 1 secret key is required."
        errors=$((errors + 1))
    fi

    if [ -n "$BUCKET2_NAME" ]; then
        if [ -z "$BUCKET2_ACCESS_KEY" ] || [ -z "$BUCKET2_SECRET_KEY" ]; then
            echo "ERROR: Bucket 2 has a name but is missing access or secret key."
            errors=$((errors + 1))
        fi
    fi

    if [ -n "$BUCKET3_NAME" ]; then
        if [ -z "$BUCKET3_ACCESS_KEY" ] || [ -z "$BUCKET3_SECRET_KEY" ]; then
            echo "ERROR: Bucket 3 has a name but is missing access or secret key."
            errors=$((errors + 1))
        fi
    fi

    if [ "$SHARING_PROTOCOL" = "samba" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        if [ -z "$SAMBA_USER" ]; then
            echo "ERROR: Samba username is required when protocol is samba or both."
            errors=$((errors + 1))
        fi
        if [ -z "$SAMBA_PASSWORD" ]; then
            echo "ERROR: Samba password is required when protocol is samba or both."
            errors=$((errors + 1))
        fi
    fi

    if [ "$errors" -gt 0 ]; then
        echo "Validation failed with $errors error(s). Aborting."
        exit 1
    fi

    echo "Input validation passed."
}

region_to_endpoint() {
    echo "https://${1}.linodeobjects.com"
}

install_system_packages() {
    echo ">>> Updating system packages..."
    apt-get update
    DEBIAN_FRONTEND=noninteractive apt-get upgrade -y

    echo ">>> Installing base dependencies..."
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
        fuse3 \
        git \
        make \
        wget \
        ufw

    if ! grep -q "^user_allow_other" /etc/fuse.conf 2>/dev/null; then
        echo "user_allow_other" >> /etc/fuse.conf
    fi

    if [ "$SHARING_PROTOCOL" = "nfs" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        echo ">>> Installing NFS server..."
        DEBIAN_FRONTEND=noninteractive apt-get install -y nfs-kernel-server
    fi

    if [ "$SHARING_PROTOCOL" = "samba" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        echo ">>> Installing Samba..."
        DEBIAN_FRONTEND=noninteractive apt-get install -y samba
    fi
}

install_go() {
    echo ">>> Installing Go ${GO_VERSION}..."
    local go_archive="go${GO_VERSION}.linux-amd64.tar.gz"
    wget -q "https://go.dev/dl/${go_archive}" -O "/tmp/${go_archive}"
    rm -rf /usr/local/go
    tar -C /usr/local -xzf "/tmp/${go_archive}"
    rm -f "/tmp/${go_archive}"
    export PATH="/usr/local/go/bin:${PATH}"
    echo 'export PATH="/usr/local/go/bin:${PATH}"' >> /etc/profile.d/golang.sh
    go version
}

build_and_install_s3gw() {
    echo ">>> Cloning s3gw repository..."
    local build_dir="/tmp/s3gw-build"
    rm -rf "$build_dir"
    git clone "$REPO_URL" "$build_dir"
    cd "$build_dir"

    echo ">>> Building s3gw..."
    make build

    echo ">>> Installing s3gw..."
    make install

    cd /
    rm -rf "$build_dir"

    echo ">>> s3gw binary installed to /usr/local/bin/s3gw"
}

generate_config() {
    echo ">>> Generating s3gw configuration..."

    local cache_bytes=$(( CACHE_SIZE_GB * 1073741824 ))

    mkdir -p "$CONFIG_DIR"
    mkdir -p "$CACHE_DIR"

    cat > "$CONFIG_FILE" <<YAML
cache_dir: ${CACHE_DIR}
max_cache_size: ${cache_bytes}
default_ttl: ${DEFAULT_TTL}
log_level: ${LOG_LEVEL}

buckets:
YAML

    add_bucket_to_config "$BUCKET1_NAME" "$BUCKET1_REGION" "$BUCKET1_ACCESS_KEY" "$BUCKET1_SECRET_KEY" "$BUCKET1_TTL" "$BUCKET1_SOLE_WRITER"

    if [ -n "$BUCKET2_NAME" ]; then
        add_bucket_to_config "$BUCKET2_NAME" "$BUCKET2_REGION" "$BUCKET2_ACCESS_KEY" "$BUCKET2_SECRET_KEY" "$BUCKET2_TTL" "$BUCKET2_SOLE_WRITER"
    fi

    if [ -n "$BUCKET3_NAME" ]; then
        add_bucket_to_config "$BUCKET3_NAME" "$BUCKET3_REGION" "$BUCKET3_ACCESS_KEY" "$BUCKET3_SECRET_KEY" "$BUCKET3_TTL" "$BUCKET3_SOLE_WRITER"
    fi

    chmod 600 "$CONFIG_FILE"
    chown root:root "$CONFIG_FILE"

    echo ">>> Configuration written to ${CONFIG_FILE}"
}

add_bucket_to_config() {
    local name="$1"
    local region="$2"
    local access_key="$3"
    local secret_key="$4"
    local ttl="$5"
    local sole_writer="$6"
    local endpoint
    endpoint=$(region_to_endpoint "$region")
    local mount_point="${MOUNT_BASE}/${name}"

    mkdir -p "$mount_point"

    cat >> "$CONFIG_FILE" <<YAML
  - name: ${name}
    region: ${region}
    endpoint: ${endpoint}
    access_key: ${access_key}
    secret_key: ${secret_key}
    mount_point: ${mount_point}
    ttl: ${ttl}
    sole_writer: ${sole_writer}
YAML
}

configure_systemd_service() {
    echo ">>> Configuring systemd service..."

    systemctl daemon-reload
    systemctl enable s3gw
}

configure_nfs() {
    echo ">>> Configuring NFS exports..."

    local exports_file="/etc/exports"

    > "$exports_file"

    add_nfs_export "$BUCKET1_NAME"

    if [ -n "$BUCKET2_NAME" ]; then
        add_nfs_export "$BUCKET2_NAME"
    fi

    if [ -n "$BUCKET3_NAME" ]; then
        add_nfs_export "$BUCKET3_NAME"
    fi

    exportfs -ra
    systemctl enable --now nfs-kernel-server

    echo ">>> NFS exports configured."
}

add_nfs_export() {
    local name="$1"
    local mount_point="${MOUNT_BASE}/${name}"
    echo "${mount_point}  ${NFS_ALLOWED_NETWORK}(rw,sync,no_subtree_check,no_root_squash,fsid=$(echo "$name" | cksum | cut -d' ' -f1))" >> /etc/exports
}

configure_samba() {
    echo ">>> Configuring Samba..."

    useradd -M -s /usr/sbin/nologin "$SAMBA_USER" 2>/dev/null || true

    (echo "$SAMBA_PASSWORD"; echo "$SAMBA_PASSWORD") | smbpasswd -s -a "$SAMBA_USER"

    local smb_conf="/etc/samba/smb.conf"

    cat >> "$smb_conf" <<EOF

# --- s3gw shares ---
EOF

    add_samba_share "$BUCKET1_NAME"

    if [ -n "$BUCKET2_NAME" ]; then
        add_samba_share "$BUCKET2_NAME"
    fi

    if [ -n "$BUCKET3_NAME" ]; then
        add_samba_share "$BUCKET3_NAME"
    fi

    systemctl enable --now smbd
    systemctl enable --now nmbd

    echo ">>> Samba configured."
}

add_samba_share() {
    local name="$1"
    local mount_point="${MOUNT_BASE}/${name}"

    cat >> /etc/samba/smb.conf <<EOF

[${name}]
   path = ${mount_point}
   browsable = yes
   writable = yes
   valid users = ${SAMBA_USER}
   force user = root
   force group = root
   create mask = 0666
   directory mask = 0777
EOF
}

configure_firewall() {
    echo ">>> Configuring firewall..."

    ufw default deny incoming
    ufw default allow outgoing
    ufw allow ssh

    if [ "$SHARING_PROTOCOL" = "nfs" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        ufw allow from "$NFS_ALLOWED_NETWORK" to any port 2049 comment "NFS"
    fi

    if [ "$SHARING_PROTOCOL" = "samba" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        ufw allow 445/tcp comment "Samba SMB"
        ufw allow 139/tcp comment "Samba NetBIOS"
    fi

    ufw --force enable

    echo ">>> Firewall configured."
}

start_services() {
    echo ">>> Starting s3gw..."
    systemctl start s3gw

    sleep 3

    if systemctl is-active --quiet s3gw; then
        echo ">>> s3gw is running."
    else
        echo "WARNING: s3gw may not have started correctly. Check: journalctl -u s3gw"
    fi
}

print_summary() {
    local ip
    ip=$(hostname -I | awk '{print $1}')

    echo ""
    echo "============================================="
    echo " s3gw Installation Complete"
    echo "============================================="
    echo ""
    echo "Service status:  systemctl status s3gw"
    echo "Logs:            journalctl -u s3gw -f"
    echo "Config file:     ${CONFIG_FILE}"
    echo "Install log:     ${LOGFILE}"
    echo ""
    echo "Mounted buckets:"
    echo "  - ${BUCKET1_NAME} -> ${MOUNT_BASE}/${BUCKET1_NAME}"

    if [ -n "$BUCKET2_NAME" ]; then
        echo "  - ${BUCKET2_NAME} -> ${MOUNT_BASE}/${BUCKET2_NAME}"
    fi
    if [ -n "$BUCKET3_NAME" ]; then
        echo "  - ${BUCKET3_NAME} -> ${MOUNT_BASE}/${BUCKET3_NAME}"
    fi

    echo ""

    if [ "$SHARING_PROTOCOL" = "nfs" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        echo "NFS client mount example:"
        echo "  mount -t nfs ${ip}:${MOUNT_BASE}/${BUCKET1_NAME} /mnt/local-mount"
        echo ""
    fi

    if [ "$SHARING_PROTOCOL" = "samba" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
        echo "Samba access:"
        echo "  Windows:  \\\\${ip}\\${BUCKET1_NAME}"
        echo "  macOS:    smb://${ip}/${BUCKET1_NAME}"
        echo "  Linux:    mount -t cifs //${ip}/${BUCKET1_NAME} /mnt/local-mount -o username=${SAMBA_USER}"
        echo ""
    fi

    echo "============================================="
}

validate_inputs
install_system_packages
install_go
build_and_install_s3gw
generate_config
configure_systemd_service

if [ "$SHARING_PROTOCOL" = "nfs" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
    configure_nfs
fi

if [ "$SHARING_PROTOCOL" = "samba" ] || [ "$SHARING_PROTOCOL" = "both" ]; then
    configure_samba
fi

configure_firewall
start_services
print_summary
