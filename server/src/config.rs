//! Loads and validates process-level server configuration.
use std::{
    env, fs,
    net::SocketAddr,
    path::{Path, PathBuf},
    time::Duration,
};

#[cfg(unix)]
use std::os::unix::fs::PermissionsExt;

use crate::{Error, Result};

const DATA_DIR_NAME: &str = ".cursor-byok-v3";
/// 便携模式下数据目录放在可执行文件同级，避免 `%USERPROFILE%` 持续增长。
const PORTABLE_DATA_DIR_NAME: &str = "data";
/// 允许显式指定数据目录；必须是绝对路径。
const DATA_DIR_ENV: &str = "CURSOR_BYOK_DATA_DIR";
const DATABASE_FILE_NAME: &str = "cursor-byok.db";
const V0049_DATA_DIR_NAME: &str = ".cursor-local-assistant-v2";
const V0049_CONFIG_FILE_NAME: &str = "config.yaml";
const DEFAULT_PROVIDER_REQUEST_TIMEOUT: Duration = Duration::from_secs(60 * 60);
const DEFAULT_PROVIDER_STREAM_IDLE_TIMEOUT: Duration = Duration::from_secs(30 * 60);

/// 解析数据目录，优先级从高到低：
/// 1. `CURSOR_BYOK_DATA_DIR` 环境变量（必须是绝对路径）；
/// 2. 可执行文件同级的 `data/` 目录（便携模式）；
/// 3. 用户主目录下的 `.cursor-byok-v3`（回退，兼容旧版本）。
///
/// 首次启用新目录时，若新位置还没有数据库而旧位置存在，则把旧目录整棵树
/// 复制过来（只复制缺失文件，不覆盖新目录里已有的内容），旧目录保留。
pub fn managed_data_dir() -> Result<PathBuf> {
    if let Some(directory) = env::var_os(DATA_DIR_ENV) {
        let directory = PathBuf::from(directory);
        if !directory.is_absolute() {
            return Err(Error::Config(format!(
                "{DATA_DIR_ENV} must be an absolute path"
            )));
        }
        prepare_data_dir(&directory)?;
        migrate_legacy_data(&directory);
        return Ok(directory);
    }
    if let Some(directory) = portable_data_dir()? {
        return Ok(directory);
    }
    legacy_data_dir()
}

/// 优先使用可执行文件同级的 `data/` 目录；目录不可写时返回 `None` 交给调用方回退。
///
/// 仅在 Windows 启用：Docker/Linux 下数据位置已有既定约定（`/data`、用户主目录），
/// 不做无谓改变。
fn portable_data_dir() -> Result<Option<PathBuf>> {
    if !cfg!(windows) {
        return Ok(None);
    }
    let Some(executable_dir) = env::current_exe()
        .ok()
        .and_then(|executable| executable.parent().map(Path::to_path_buf))
    else {
        return Ok(None);
    };
    let directory = executable_dir.join(PORTABLE_DATA_DIR_NAME);
    if prepare_data_dir(&directory).is_err() {
        return Ok(None);
    }
    migrate_legacy_data(&directory);
    Ok(Some(directory))
}

fn legacy_data_dir_path() -> Option<PathBuf> {
    dirs::home_dir().map(|home| home.join(DATA_DIR_NAME))
}

fn legacy_data_dir() -> Result<PathBuf> {
    let data_dir = legacy_data_dir_path()
        .ok_or_else(|| Error::Config("cannot resolve user home directory".into()))?;
    prepare_data_dir(&data_dir)?;
    Ok(data_dir)
}

/// 创建目录并做一次写入探测，确保目录真的可写（只读目录 `create_dir_all` 也会成功）。
fn prepare_data_dir(directory: &Path) -> std::io::Result<()> {
    fs::create_dir_all(directory)?;
    let probe = directory.join(".write-probe");
    fs::write(&probe, b"")?;
    let _ = fs::remove_file(&probe);
    #[cfg(unix)]
    fs::set_permissions(directory, fs::Permissions::from_mode(0o700))?;
    Ok(())
}

/// 新位置缺少数据库、且旧位置存在数据库时才需要迁移。
fn should_migrate_legacy(legacy: &Path, destination: &Path) -> bool {
    legacy.join(DATABASE_FILE_NAME).is_file() && !destination.join(DATABASE_FILE_NAME).exists()
}

fn migrate_legacy_data(destination: &Path) {
    let Some(legacy) = legacy_data_dir_path() else {
        return;
    };
    if !should_migrate_legacy(&legacy, destination) {
        return;
    }
    if let Err(error) = copy_missing_tree(&legacy, destination) {
        tracing::warn!(
            %error,
            from = %legacy.display(),
            to = %destination.display(),
            "failed to migrate legacy data directory"
        );
    }
}

/// 递归复制缺失的文件，不覆盖目标位置已有内容。
fn copy_missing_tree(source: &Path, destination: &Path) -> std::io::Result<()> {
    fs::create_dir_all(destination)?;
    for entry in fs::read_dir(source)? {
        let entry = entry?;
        let target = destination.join(entry.file_name());
        let file_type = entry.file_type()?;
        if file_type.is_dir() {
            copy_missing_tree(&entry.path(), &target)?;
        } else if file_type.is_file() && !target.exists() {
            fs::copy(entry.path(), &target)?;
        }
    }
    Ok(())
}

pub fn v0049_config_path() -> Result<PathBuf> {
    let home_dir = dirs::home_dir()
        .ok_or_else(|| Error::Config("cannot resolve user home directory".into()))?;
    Ok(home_dir
        .join(V0049_DATA_DIR_NAME)
        .join(V0049_CONFIG_FILE_NAME))
}

#[derive(Clone, Debug, PartialEq, Eq)]
pub enum ProviderKind {
    OpenAiChat,
    OpenAiResponses,
    Anthropic,
}

#[derive(Clone)]
pub struct ProviderConfig {
    pub kind: ProviderKind,
    pub request_url: String,
    pub api_key: String,
    pub custom_headers: reqwest::header::HeaderMap,
    pub max_output_tokens: Option<u64>,
    pub request_timeout: Duration,
    pub allowed_body_fields: Option<std::collections::HashSet<String>>,
}

#[derive(Clone)]
pub struct Config {
    pub listen_addr: SocketAddr,
    pub database_url: String,
    pub provider_request_timeout: Duration,
    pub provider_stream_idle_timeout: Duration,
    pub console: Option<ConsoleSource>,
    pub use_persisted_ports: bool,
    /// 面向用户的应用版本;桌面壳会覆盖为自身版本,用于插件 minAppVersion 门控。
    pub app_version: String,
}

#[derive(Clone)]
pub enum ConsoleSource {
    Directory(PathBuf),
    Proxy(url::Url),
}

impl Config {
    pub fn from_env() -> Result<Self> {
        let listen_addr = env::var("CURSOR_LISTEN_ADDR")
            .unwrap_or_else(|_| "127.0.0.1:3000".into())
            .parse()
            .map_err(|error| Error::Config(format!("invalid CURSOR_LISTEN_ADDR: {error}")))?;
        let request_timeout = match env::var("CURSOR_PROVIDER_TIMEOUT_SECONDS") {
            Ok(value) => Duration::from_secs(value.parse().map_err(|error| {
                Error::Config(format!("invalid CURSOR_PROVIDER_TIMEOUT_SECONDS: {error}"))
            })?),
            Err(env::VarError::NotPresent) => DEFAULT_PROVIDER_REQUEST_TIMEOUT,
            Err(error) => {
                return Err(Error::Config(format!(
                    "invalid CURSOR_PROVIDER_TIMEOUT_SECONDS: {error}"
                )))
            }
        };
        let console_dir = env::var_os("CURSOR_CONSOLE_DIR").map(PathBuf::from);
        let console_proxy = env::var("CURSOR_CONSOLE_PROXY")
            .ok()
            .map(|value| {
                value.parse().map_err(|error| {
                    Error::Config(format!("invalid CURSOR_CONSOLE_PROXY: {error}"))
                })
            })
            .transpose()?;
        let console = match (console_dir, console_proxy) {
            (Some(_), Some(_)) => {
                return Err(Error::Config(
                    "CURSOR_CONSOLE_DIR and CURSOR_CONSOLE_PROXY cannot both be set".into(),
                ))
            }
            (Some(directory), None) => Some(ConsoleSource::Directory(directory)),
            (None, Some(proxy)) => Some(ConsoleSource::Proxy(proxy)),
            (None, None) => None,
        };
        Ok(Self {
            listen_addr,
            database_url: database_url_from_env()?,
            provider_request_timeout: request_timeout,
            provider_stream_idle_timeout: DEFAULT_PROVIDER_STREAM_IDLE_TIMEOUT,
            console,
            use_persisted_ports: false,
            app_version: env!("CARGO_PKG_VERSION").into(),
        })
    }

    pub fn desktop() -> Result<Self> {
        Ok(Self {
            listen_addr: "127.0.0.1:0"
                .parse()
                .expect("desktop listen address is static"),
            database_url: default_database_url()?,
            provider_request_timeout: DEFAULT_PROVIDER_REQUEST_TIMEOUT,
            provider_stream_idle_timeout: DEFAULT_PROVIDER_STREAM_IDLE_TIMEOUT,
            console: None,
            use_persisted_ports: true,
            app_version: env!("CARGO_PKG_VERSION").into(),
        })
    }
}

fn database_url_from_env() -> Result<String> {
    match env::var("CURSOR_DATABASE_URL") {
        Ok(database_url) => Ok(database_url),
        Err(env::VarError::NotPresent) => default_database_url(),
        Err(error) => Err(Error::Config(format!(
            "invalid CURSOR_DATABASE_URL: {error}"
        ))),
    }
}

fn default_database_url() -> Result<String> {
    let data_dir = managed_data_dir()?;
    database_url_for_dir(&data_dir)
}

fn database_url_for_dir(data_dir: &std::path::Path) -> Result<String> {
    let database_path = data_dir.join(DATABASE_FILE_NAME);
    let database_path = database_path
        .to_str()
        .ok_or_else(|| Error::Config("database path is not valid UTF-8".into()))?;
    Ok(format!("sqlite://{database_path}"))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn provider_timeout_defaults_match_runtime_boundaries() {
        assert_eq!(
            DEFAULT_PROVIDER_STREAM_IDLE_TIMEOUT,
            Duration::from_secs(30 * 60)
        );
        assert_eq!(
            DEFAULT_PROVIDER_REQUEST_TIMEOUT,
            Duration::from_secs(60 * 60)
        );
    }

    #[test]
    fn legacy_migration_requires_a_missing_destination_database() {
        let directory = tempfile::tempdir().unwrap();
        let legacy = directory.path().join("legacy");
        let destination = directory.path().join("portable");
        fs::create_dir_all(&legacy).unwrap();
        fs::create_dir_all(&destination).unwrap();

        // 旧位置没有数据库：不迁移。
        assert!(!should_migrate_legacy(&legacy, &destination));

        // 旧位置有数据库、新位置没有：迁移。
        fs::write(legacy.join(DATABASE_FILE_NAME), b"legacy").unwrap();
        assert!(should_migrate_legacy(&legacy, &destination));

        // 新位置已经有数据库：不再迁移，避免覆盖新数据。
        fs::write(destination.join(DATABASE_FILE_NAME), b"current").unwrap();
        assert!(!should_migrate_legacy(&legacy, &destination));
    }

    #[test]
    fn copy_missing_tree_copies_nested_files_without_overwriting() {
        let directory = tempfile::tempdir().unwrap();
        let source = directory.path().join("source");
        let destination = directory.path().join("destination");
        fs::create_dir_all(source.join("cache")).unwrap();
        fs::create_dir_all(&destination).unwrap();
        fs::write(source.join("cursor-byok.db"), b"legacy").unwrap();
        fs::write(source.join("cache/page.txt"), b"cached").unwrap();
        fs::write(destination.join("cursor-byok.db"), b"current").unwrap();

        copy_missing_tree(&source, &destination).unwrap();

        // 已存在的文件保持不变,缺失的文件被补上,子目录一并复制。
        assert_eq!(
            fs::read(destination.join("cursor-byok.db")).unwrap(),
            b"current"
        );
        assert_eq!(
            fs::read(destination.join("cache/page.txt")).unwrap(),
            b"cached"
        );
    }
}
