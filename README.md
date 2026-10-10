<p align="center">
  <img src="web/logo.svg" width="96" alt="simple-pm2-monitor">
</p>

# Simple PM2 Monitor

A dependency-free Go web dashboard for managing .NET and Next.js projects on a Debian development server. The UI is embedded in the Go executable, so the dashboard needs no Node.js runtime, database, or separate frontend build. Running your projects requires PM2 and their .NET/Node.js tools.

- Group projects and start, stop, or restart a group or an individual project.
- View status, working directory, configured port, and PM2 process CPU/RAM usage.
- Refresh automatically every five seconds; search projects and filter running projects.
- Discover existing PM2 processes automatically, with read-only access to unmanaged processes.
- Read the last 64 KiB of stdout/stderr log files for each process instance, and clear a managed instance's logs when needed.
- Track HTTP requests per second using an optional Prometheus counter.
- Add projects through a form, discover settings from .NET/Next.js files, and choose an existing group or type a new group name.
- Edit or delete individual projects directly from their rows.
- Edit groups and startup order in the JSON configuration editor.

**All dashboard access requires the admin credentials saved in `projects.json`.** The first-run wizard asks for a username and password and saves them in that file. The same login works when listening on `0.0.0.0`. Authentication settings are read only from JSON; environment variables and Bearer tokens are not supported. See [Access over the network](#access-over-the-network).

## Build and run

Go 1.22 or later is required. Install PM2 under **the same user account that runs your projects**. PM2 processes belong to a user and a `PM2_HOME` directory; running the dashboard as root may connect to a different PM2 environment.

```bash
go build -o simple-pm2-monitor .
./simple-pm2-monitor -config ./projects.json
```

If `projects.json` does not exist, a terminal setup wizard asks for:

1. The listening IP address, defaulting to `127.0.0.1`.
2. The listening port, defaulting to `8989`.
3. An admin username, defaulting to `admin`.
4. An admin password, which must not be empty.
5. Whether to run the dashboard as a systemd user service, defaulting to no.
6. Whether you want to add project settings now, defaulting to no.

Press Enter to accept a default. The password has no default and must be entered. If you choose to add a project, the wizard asks for its name, group, working directory, command, JSON argument array, environment variables, and project port. You can add multiple projects or configure them later through the web UI. Added projects are not started automatically. The terminal prompts and UI are in English. Use `y`/`yes` or `n`/`no` for setup questions.

After setup, the listening address, admin credentials, and projects are saved atomically with `0600` file permissions. The username and password are stored as **plain text**, as intended for this development tool; keep the configuration private. Local `projects*.json` files are excluded by `.gitignore`, while `projects.example.json` is kept as a shareable template. Never put your actual password in the example file. Incomplete or interrupted setup does not create a configuration file. Subsequent launches use the saved `listen` address without asking again. Files without `listen` use `127.0.0.1:8989`. Existing files without admin credentials must be updated with both `adminUsername` and `adminPassword` before startup. An explicitly supplied `-listen` flag overrides an existing file's address for that run only. During first-run setup, it supplies the prompt defaults instead.

For systemd or automation without terminal input:

```bash
./simple-pm2-monitor -non-interactive -config ./projects.json
```

This option requires an existing JSON configuration containing `adminUsername` and `adminPassword`. If the file is missing, startup stops with an instruction to run terminal setup or create the file manually. No credentials are read from environment variables. You can copy `projects.example.json` to `projects.json`, replace its example credentials, and adjust the paths before running a service.

The listening address and admin credentials are stored as:

```json
{
  "listen": "127.0.0.1:8989",
  "adminUsername": "admin",
  "adminPassword": "replace-with-your-password",
  "projects": []
}
```

You can change `listen` through the web UI's JSON editor; the new address takes effect after restarting the dashboard. Adding a project through the form preserves the existing listening setting and admin credentials. Admin credential changes made in the JSON editor take effect immediately; sign in again with the new values. For an existing configuration created before admin login was added, add `adminUsername` and `adminPassword` manually and restart the dashboard. Existing configurations are not overwritten or automatically sent through the wizard again.

The dashboard listens on `127.0.0.1:8989` by default. Open an SSH tunnel from your own computer:

```bash
ssh -N -L 8989:127.0.0.1:8989 dev@debian-server
```

Then open `http://localhost:8989` in your browser. VS Code Remote SSH port forwarding also works.

Example with custom paths and a different listening address:

```bash
./simple-pm2-monitor -listen 127.0.0.1:9090 -config /home/dev/projects.json -pm2 /absolute/path/to/pm2
```

### Access over the network

`0.0.0.0` binds to all IPv4 network interfaces. Since the dashboard can run commands and manage processes under its OS user's permissions, all addresses, including loopback, require the admin credentials stored in JSON.

For a new installation, launch:

```bash
./simple-pm2-monitor -listen 0.0.0.0:8989
```

Accept `0.0.0.0` in the first-run wizard, choose the port, and enter an admin username and password. They are saved in plain text in `projects.json` and read from that file on subsequent launches.

For an existing file, make sure it contains both `adminUsername` and `adminPassword`. `-listen` overrides its address for this run. For future launches without that flag, save `"listen": "0.0.0.0:8989"` in the configuration.

From another computer, open `http://SERVER_IP:8989`, using the server's actual reachable IP address rather than `0.0.0.0`. Click **Admin login** and enter the credentials saved during setup. Authentication uses HTTP Basic authorization, and the browser keeps the authorization value only in that tab's `sessionStorage`.

If an existing installation refuses to start, check that its configuration contains both admin fields. An environment variable cannot provide or override these credentials. For systemd, use the same JSON file as your terminal setup.

Use an SSH tunnel, or a TLS reverse proxy and a private network, rather than exposing the dashboard directly to the internet. A reverse proxy must forward consistent `Host` and `Origin` values.

## Configure projects

### Add, edit, and delete projects

Click **Add project** to create a project, or **Edit** on a managed project's row to update its saved name, group, working directory, command, arguments, environment, port, and metrics settings. Edits replace that project's entry in place, preserving startup order. Duplicate project names are rejected. You can use file discovery while editing too.

Group, displayed port, and metrics changes take effect immediately. Saving a project does not alter an existing PM2 record. A renamed process can therefore appear under its previous name as unmanaged; the new project name is registered when you start it. With an explicit `watchFiles` value, Start/Restart applies launch settings as described below. Otherwise, use the process-update steps below.

Click **Delete** and confirm to remove a project from the dashboard configuration. Deletion preserves other projects and admin/listening settings. It does not stop or delete PM2 processes or remove files. Those processes appear as unmanaged after deletion. Stop the project before deleting its configuration if you want it to stop running.

The individual-project API uses authenticated `POST`, `PUT`, and `DELETE` requests to `/api/project`. Add with `{"project": {...}}`, edit with `{"name": "original-name", "project": {...}}`, and delete with `{"name": "project-name"}`. Changes are saved atomically to JSON; project operations never invoke PM2 commands automatically.

### Read settings from project files

In **Add project**, enter the absolute path of a folder or project file **on the development server** and click **Read settings**. Choose a discovered project/profile or npm script from the dropdown. The form fills in the project name, working directory, command, arguments, environment, and port. Review and edit these values before saving. Discovery only reads files; it does not run scripts, build projects, register PM2 processes, or start services.

Use the **...** button beside either path field to browse the server. The project settings picker shows directories and supported project files; the working directory picker shows directories only. The first visit starts in the dashboard user's home directory (`HOME`). Later visits start in the last successfully browsed directory, remembered in this browser's local storage across sessions. An inaccessible saved directory falls back to the home directory. **Home**, **Up**, direct path entry, and **Show hidden entries** help navigate. Browsing uses the dashboard OS user's filesystem permissions and requires admin login. At most 5,000 entries are inspected per directory; enter a narrower path for larger directories. Selecting a path fills the field; click **Read settings** separately to inspect project settings.

- **.NET:** Accepts `.sln`, `.slnx`, `.csproj`, `.fsproj`, and `.vbproj`. Solution references locate the actual project directories, including Windows-style paths. Runnable projects are identified through their Web SDK, executable output type, or `Project` launch profiles. Library projects are skipped with a notice. Profiles from `Properties/launchSettings.json` are listed separately; IIS Express profiles are omitted. The selected profile's URL determines the suggested port, preferring HTTP when both HTTP and HTTPS are listed. Without a profile port, the reader checks the selected environment's `appsettings.<Environment>.json`, then `appsettings.json`, for `Kestrel.Endpoints` or `Urls`. Comments and trailing commas in .NET settings are supported. See [Microsoft's launch-profile behavior](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-run).
- **Next.js:** Reads `package.json` and lists its `scripts`, with `dev` and `start` first. Selecting a target generates `npm run <target>`. Literal `--port`/`-p` options and inline `PORT=...` assignments are recognized for `next dev/start`; otherwise the standard CLI default of 3000 is suggested. Dynamic port arguments and custom launchers leave the port unknown. `build` and `lint` remain selectable but can be one-off tasks rather than servers. `next.config.js` is not executed, and `.env` files are not used to infer the CLI port: Next.js documents that `PORT` cannot be set through `.env`. See the [Next.js CLI reference](https://nextjs.org/docs/app/api-reference/cli/next).

The group field is an editable combobox: choose a previously saved group from the suggestions or enter a new name. Suggestions refresh when the add-project dialog opens; the last saved group stays selected for the next addition.

Folder discovery scans up to four directory levels and 4,000 filesystem entries, skips hidden/build/dependency directories, and returns at most 200 options. Each configuration file is limited to 2 MiB. For larger repositories, supply a narrower directory or a solution/project file directly. Detection is static; imported MSBuild settings, inherited environment variables, shell expansion, custom scripts, or application code can override the suggested values. Unknown ports remain editable rather than being presented as measured values.

Example for .NET development:

```json
{
  "name": "shop-api",
  "group": "shop",
  "cwd": "/home/dev/projects/shop-api",
  "command": "/usr/bin/dotnet",
  "args": ["run", "--urls", "http://0.0.0.0:5001"],
  "env": { "ASPNETCORE_ENVIRONMENT": "Development" },
  "watchFiles": false,
  "port": 5001,
  "metricsUrl": "",
  "requestMetric": "http_requests_total"
}
```

For a published .NET application, set `args` to `["/home/dev/projects/shop-api/publish/Shop.Api.dll"]`. For Next.js, use npm's full path as `command` and `["run", "dev", "--", "--port", "3000"]` as `args`. Prepare dependencies (`npm install`, `dotnet restore`) beforehand.

Commands and arguments are separate; the dashboard does not invoke a shell. Operators such as `&&`, pipes, and shell variables are not automatically interpreted in arguments. If needed, supply your own launcher script as the command. Use `command -v dotnet`, `command -v npm`, and `command -v pm2` to find the actual executable paths. With NVM, add the Node.js directory to the service's `PATH` so npm can find `node`.

`port` controls the displayed port and its clickable link; also pass the port to your application through command arguments or `env`. The dashboard does not check whether that port is listening. `online` is a PM2 status, not confirmation that an HTTP endpoint is ready.

Click a project's port to open its HTTP root URL in a new browser tab. The link uses the hostname/IP from the browser's dashboard address and the configured project port. For example, `http://192.168.1.10:8989` with project port `5180` opens `http://192.168.1.10:5180/`. Hostnames, IPv4, and IPv6 addresses are supported. Dashboard paths, query strings, and its port are not carried over. The generated link uses HTTP because a port alone cannot determine the application's protocol; HTTPS-only services need their actual HTTPS URL.

With an SSH tunnel to `localhost:8989`, project links also use localhost: forward each project port separately, for example `ssh -N -L 8989:127.0.0.1:8989 -L 5180:127.0.0.1:5180 dev@your-server`. A reverse proxy for the dashboard does not automatically expose project ports on the same hostname. The application must listen on an address reachable through your chosen connection. These links do not open firewall ports, forward traffic, or check endpoint readiness.

### File watching and manual restarts

The **Watch files** checkbox appears for `dotnet run` / `dotnet watch run` and Next.js `npm run dev` / `npm run start` commands. It is saved as the optional boolean `watchFiles` in each project entry.

- **.NET, disabled:** uses `dotnet run`, builds the project on launch, and does not run the code watcher. Use **Restart** to rebuild and apply code changes. Newly discovered .NET projects default to this mode, avoiding additional `dotnet watch` inotify usage. Application-defined watchers, such as configuration reload watchers, are independent of this setting.
- **.NET, enabled:** uses `dotnet watch run` with hot reload. Project paths and launch-profile arguments are retained. Automatic browser launching stays disabled for discovered projects.
- **Next.js, enabled:** runs the existing `dev` script and sets `NODE_ENV=development`.
- **Next.js, disabled:** runs the existing `start` script and sets `NODE_ENV=production`. Both scripts must have their usual Next.js behavior. Run `npm run build` in the project's directory before the first start and after code changes, then use **Restart**. The dashboard does not build Next.js automatically. Custom script targets and other command wrappers must be configured manually.

Saving does not start or restart a process. For .NET projects and other projects with an explicit `watchFiles` value, **Restart** applies the effective command, arguments, directory, and environment through the PM2 ecosystem configuration, preserving existing process IDs. **Start** also updates existing records if their arguments differ from the selected mode; this restarts them. With unchanged arguments, Start keeps its usual behavior. You do not need to delete PM2 records to change modes. Multiple records with the same managed project name receive the same launch settings.

Existing configurations without `watchFiles` retain their original watching mode until you save an explicit selection. .NET launch settings are reapplied on Restart to ensure the PM2 environment workaround below is installed; other commands retain their existing PM2 update behavior. Unknown commands are not rewritten. If watching is enabled and logs report an inotify instance limit, increase the server's limit or disable **Watch files** and use manual restarts.

### .NET restore failures caused by PM2 metadata

PM2 can inject a lowercase `version` environment variable with its detected package version, often `N/A` for .NET projects. MSBuild imports environment variables as properties, so this value can override the default `Version` and cause NuGet restore to fail with `MSB4181: The "RestoreTask" task returned false but did not log an error`. In a verified case, the same restore succeeded outside PM2 and when only that variable was removed.

The dashboard launches .NET through `env -u version dotnet ...`, using a direct argument array without a shell. This removes PM2's lowercase metadata variable before .NET starts, preserves other environment variables, and leaves project version settings in the `.csproj` unchanged. Both watched and unwatched modes use this wrapper. The standard `env` executable is required and is available on Debian. PM2 may show `env` as the executable even though the running process is .NET.

After upgrading the dashboard, use **Restart** or **Start** on a .NET project to apply the wrapper to its existing PM2 record. The record and its ID are preserved. Old messages remain in PM2's append-only log files; check newly appended output before concluding that an earlier error is still occurring. This workaround addresses the confirmed environment collision; other restore failures require their own diagnosis. See [MSBuild environment properties](https://learn.microsoft.com/en-us/visualstudio/msbuild/how-to-use-environment-variables-in-a-build) and [MSB4181 diagnostics](https://learn.microsoft.com/en-us/visualstudio/msbuild/errors/msb4181).

### .NET browser launch warnings on a server

If logs show `Failed to launch 'http://.../swagger'` and `Unable to launch the browser`, check whether the application also reports `Now listening on`. The missing executable in that warning is the browser launcher, even when the message mentions an existing working directory. A server without a desktop browser can run the API successfully while browser launching fails. Not all messages written to standard error indicate an application failure, and PM2 log files can contain output from earlier runs.

Newly discovered .NET projects set `DOTNET_WATCH_SUPPRESS_LAUNCH_BROWSER=1` to disable automatic browser launching while retaining code hot reload. See [Microsoft's dotnet watch environment variables](https://learn.microsoft.com/en-us/dotnet/core/tools/dotnet-watch#environment-variables).

For an existing project, set `"launchBrowser": false` in its selected profile in `Properties/launchSettings.json`, then stop and start the project. Alternatively, add `"DOTNET_WATCH_SUPPRESS_LAUNCH_BROWSER": "1"` to the dashboard project's environment and recreate its PM2 record as described below; for projects without an explicit `watchFiles` setting, saving an environment change and clicking Restart does not update an existing PM2 record. With `watchFiles` configured, Restart applies the saved launch settings directly. Use the server's actual hostname/IP to open Swagger from your own browser, for example `http://your-server:5180/swagger`; `0.0.0.0` is a listening address.

### Group operations and existing projects

The order of the `projects` array determines startup and restart order. Projects stop in reverse order. If startup or restart fails, subsequent projects are skipped and their results are displayed; stopping continues with other projects. A group request has a 75-second time limit, and each PM2 command has a 15-second limit. The dashboard does not wait for dependencies to become HTTP-ready; applications that depend on other services should retry connections.

Process behavior:

- If no PM2 record exists, starting creates and launches one through a temporary ecosystem JSON file.
- If a record exists, starting applies `start` to the matching PM2 IDs.
- Restarting also registers and launches a project that has no PM2 record.
- Stopping a project with no PM2 record succeeds without taking action.

New PM2 records use `interpreter: none`, `exec_mode: fork`, and `autorestart: false`. This prevents failed development services from continuously restarting; dotnet watch/Next.js manage their own code reloads.

To manage an existing process, add a project with **the same PM2 name**. All matching instances are managed, so keep project names unique in your PM2 environment. Unmanaged processes are not automatically stopped or changed.

For non-.NET projects without an explicit `watchFiles` setting, changes to a project's command, directory, or environment **do not update an existing PM2 record**. Those settings are used when a new record is created. To update an existing record, stop the project, remove the relevant record from the terminal with `pm2 delete <id>`, and then start it again through the dashboard. Changing only the group takes effect immediately in the dashboard; the existing PM2 namespace is not updated. Removing a project from configuration does not delete or stop its PM2 process. Restart the dashboard after editing the file manually; changes made through the UI take effect immediately, except for the listening address.

The dashboard does not automatically run `pm2 save`, `pm2 startup`, or `pm2 delete`, or update existing process environments. Configure PM2's startup/save commands separately if you want projects to run after a server reboot.

## Viewing and clearing logs

The **Logs** dialog reads the stdout and stderr files reported by PM2 for the selected process ID. Their default location is `$PM2_HOME/logs` (usually `~/.pm2/logs`), but PM2 can use custom paths. The dashboard shows the last 64 KiB of each file. PM2 normally appends output across restarts, so old failures can remain visible alongside newer successful runs. This is separate from the dashboard's systemd journal or application-managed log files.

Choose an instance in the dialog, click **Clear Logs**, and confirm to permanently remove the existing contents of that instance's stdout and stderr files. Clearing truncates the existing files rather than deleting or replacing them, so the process can keep running and writing to the same files. The dialog refreshes afterward; new output may appear immediately. It does not clear PM2's daemon log, rotated archives, or the application's other log files.

Clearing requires admin login and a managed project. Unmanaged process logs remain read only. The server resolves file paths from PM2's current process list; the browser cannot submit arbitrary paths. If another PM2 instance shares the same log file, clearing is refused to avoid removing that instance's history too. Non-regular files and symlink paths are also refused. The dashboard OS user needs write permission on the existing log files. Clearing is only performed when you request and confirm it.

## Metrics and limitations

**CPU/RAM:** Values come from the `monit` fields in PM2 `jlist`; instances with the same name are summed. They describe the main processes PM2 monitors. Child processes launched by npm or dotnet watch may be omitted, so these values do not represent the entire process tree. See [PM2 monitoring](https://pm2.keymetrics.io/docs/usage/monitoring/) and [PM2 API fields](https://pm2.keymetrics.io/docs/usage/pm2-api/).

**HTTP requests/s:** If your application exposes a Prometheus endpoint, fill in `metricsUrl` and `requestMetric`. For example, an endpoint at `http://127.0.0.1:5001/metrics` might return:

```text
http_requests_total{method="GET",status="200"} 120
http_requests_total{method="POST",status="200"} 35
```

The dashboard sums labeled series with the configured metric name and divides the difference between consecutive counter values by elapsed time. It displays `—` on the first sample, after a counter reset, or when the metric is missing. Access or metric errors appear in the cell's tooltip. This reflects the HTTP load measured by the application. Authenticated metrics endpoints are not supported in this version.

**Network:** PM2 does not provide network bytes per second for individual processes. This version does not measure network traffic. Debian `/proc/<pid>/net/dev` values generally cover an entire network namespace; presenting them as project traffic would be misleading. Accurate process-level measurements require a separate eBPF/cgroup collector or application/proxy instrumentation.

### Quick start: application metrics

**A Prometheus server is optional for this dashboard.** The dashboard reads each application's `/metrics` endpoint directly and calculates requests/s itself. Install Prometheus if you also want stored time series and queries. Installing the server alone does not add request metrics to your applications.

For an ASP.NET Core application, run this in the directory containing its `.csproj`:

```bash
dotnet add package prometheus-net.AspNetCore
```

In `Program.cs`, import `Prometheus` and add the middleware and endpoint to your existing application pipeline:

```csharp
using Prometheus;

// Keep your existing builder and service registrations.
var app = builder.Build();
app.UseRouting();
app.UseHttpMetrics();
// Keep your existing authentication, authorization, and route mappings here.
app.MapMetrics();
app.Run();
```

Merge these lines into the existing pipeline; do not create a second `app` or duplicate `app.Run()`. Place `UseHttpMetrics()` after routing and before the endpoints being measured. See the [prometheus-net integration guide](https://github.com/prometheus-net/prometheus-net#aspnet-core-http-request-metrics).

Restart the application, send it a few normal HTTP requests, and check its metrics from the development server:

```bash
curl -fsS http://127.0.0.1:5001/metrics
```

In the dashboard's **Edit** form, set **Prometheus metrics URL** to `http://127.0.0.1:5001/metrics` and **Request counter name** to `http_requests_received_total`. Replace `5001` with your application's port. That counter name is the [prometheus-net default](https://github.com/prometheus-net/prometheus-net/blob/master/Prometheus.AspNetCore/HttpMetrics/HttpRequestCountMiddleware.cs); other libraries can use different names. Wait for two dashboard refreshes to see a rate.

For Next.js, install `prom-client` with `npm install prom-client` in the Next.js project. You must implement a server-side metrics route that returns `await registry.metrics()` with `registry.contentType`, and increment a shared `Counter` such as `http_requests_total` in the request handlers you want to measure. Installing the package or exposing default process metrics does not automatically count all Next.js requests. Use the Node.js runtime for the metrics route; counters must persist across requests rather than being recreated inside each handler. See the [Node.js client documentation](https://github.com/prometheus/client_js). Then set the dashboard fields to your endpoint, for example `http://127.0.0.1:3000/api/metrics`, and the counter's exact name. Separate worker processes have separate counters; one worker's endpoint does not provide an aggregate for every worker.

Keep metrics endpoints reachable by the dashboard and local collectors. If an application listens on all interfaces, using `127.0.0.1` in the dashboard does not prevent others from accessing that application's metrics endpoint; restrict access through the application's listener, firewall, or reverse proxy. The dashboard currently cannot send credentials to a protected metrics endpoint.

### Optional: install Prometheus on Debian

The [Debian Prometheus package](https://packages.debian.org/stable/prometheus) provides the server and its systemd service:

```bash
sudo apt update
sudo apt install prometheus
```

For local development, bind its web interface to localhost. Run `systemctl cat prometheus` to inspect the installed service. Debian's [packaged service](https://sources.debian.org/src/prometheus/2.42.0%2Bds-5%2Bdeb12u1/debian/service/) reads `ARGS` from `/etc/default/prometheus`. Edit that file:

```bash
sudoedit /etc/default/prometheus
```

Add `--web.listen-address=127.0.0.1:9090` to the existing `ARGS` value, preserving any other options and replacing an existing listen-address flag. If there are no existing options, use:

```text
ARGS="--web.listen-address=127.0.0.1:9090"
```

Edit `/etc/prometheus/prometheus.yml` and add an application job to the **existing** `scrape_configs` list. Do not create a second `scrape_configs` key or remove the packaged jobs:

```yaml
scrape_configs:
  # Keep the existing jobs here.
  - job_name: shop-api
    scrape_interval: 15s
    metrics_path: /metrics
    static_configs:
      - targets: ["127.0.0.1:5001"]
```

For Next.js, add another job with its port and actual metrics path, such as `/api/metrics`. Validate the configuration, enable the service, and apply changes:

```bash
promtool check config /etc/prometheus/prometheus.yml
sudo systemctl enable --now prometheus
sudo systemctl restart prometheus
systemctl status prometheus --no-pager
curl -fsS http://127.0.0.1:9090/-/ready
```

On your own computer, forward the web interface over SSH (replace the username and server):

```bash
ssh -N -L 9090:127.0.0.1:9090 dev@your-server
```

Open `http://127.0.0.1:9090` in your local browser and check the target status page. Once the application has handled requests and several scrapes have completed, query:

```promql
sum(rate(http_requests_received_total{job="shop-api"}[1m]))
```

Use your actual counter and job names. Depending on your instrumentation, metrics scraping requests can also contribute to the request count. Prometheus stores the history independently; keep the dashboard's `metricsUrl` pointed at the **application**, not port `9090` or Prometheus's query API. See the [official Prometheus setup guide](https://prometheus.io/docs/prometheus/latest/getting_started/).

If a target is down, first check the application's metrics URL with `curl` on the server. If the dashboard reports `counter not found`, check the exact exported counter name and generate a request to an instrumented endpoint. For Prometheus service errors, use `journalctl -u prometheus -n 50 --no-pager` and rerun `promtool check config`. None of these steps adds per-project network measurements to this dashboard.

## Keep the dashboard running with systemd

Choose **Run as service** during first-run setup, or open **Configuration → Run as service** later. This installs and enables a systemd user service under the current OS user, preserving the PM2 executable, `PATH`, and `PM2_HOME`. The dashboard releases its HTTP port before starting the service; the web UI reconnects automatically. A working Linux systemd user manager is required. If first-run installation fails, the saved configuration is kept and the dashboard starts in the foreground with an error message.

The default installed files are:

- Unit: `~/.config/systemd/user/simple-pm2-monitor.service`.
- Executable copy: `~/.local/share/simple-pm2-monitor/simple-pm2-monitor`.

Absolute `XDG_CONFIG_HOME` and `XDG_DATA_HOME` values override these locations. The executable copy also makes installation from `go run` possible. The service reads the same absolute JSON configuration path; credentials are not copied into the unit. Service status comes from systemd rather than a JSON setting. An explicit command-line `-listen` override is retained in the unit; otherwise, the saved JSON address applies on restart. Custom units with the same name are not overwritten.

**Configuration** shows service status, its unit path, and whether user lingering is enabled. **Disable autostart** disables future automatic starts while leaving the current dashboard running. To start the user manager at boot and keep it running after logout, enable lingering for your OS account. For example:

```bash
sudo loginctl enable-linger coolwolf
```

Replace `coolwolf` with your OS username. This administrator command is shown as a hint; the dashboard does not run it automatically. See the [loginctl documentation](https://manpages.debian.org/unstable/systemd/loginctl.1.en.html).

Manage the installed user service from a terminal with:

```bash
systemctl --user status simple-pm2-monitor
journalctl --user -u simple-pm2-monitor -f
systemctl --user restart simple-pm2-monitor
# Disable autostart and stop the dashboard immediately:
systemctl --user disable --now simple-pm2-monitor
```

To update the installed executable, stop the user service, rebuild and launch the new repository executable in the foreground with the same configuration, then choose **Run as service** again. Rebuilding the repository alone does not replace the installed copy. This service keeps the dashboard running; it does not start your projects at boot.

### Manual system service alternative

Edit the user, executable/configuration paths, and `PATH` in `deploy/simple-pm2-monitor.service`. The dashboard must run as the same user and with the same `PM2_HOME` as PM2. Complete setup in a terminal before enabling the service, or prepare the JSON file manually. The service uses `-non-interactive` and reads its listening address and admin credentials from that file. It refuses to start if the file is missing or its credentials are incomplete.

Keep `projects.json` private with `0600` file permissions and outside version control. Set its saved `listen` address to `0.0.0.0:8989` if you want the service to listen on all IPv4 interfaces.

Example installation:

```bash
sudo cp deploy/simple-pm2-monitor.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now simple-pm2-monitor
journalctl -u simple-pm2-monitor -f
```

This service keeps only the dashboard running; it does not start your projects at boot. The sample assumes the executable is at `/home/dev/bin/simple-pm2-monitor`.

## Validation

```bash
go test -race ./...
go vet ./...
go build .
node --check web/app.js
node --test tests/auth-ui.test.cjs
```

The default tests mock PM2 commands and do not start or stop real projects. They cover first-run setup, invalid input retries, interrupted setup, saved-address/CLI precedence, address and admin credential preservation during web configuration saves, admin login and password changes, rejection of Bearer authentication and environment credential overrides, access controls, atomic configuration persistence, group ordering and failures, PM2 ecosystem generation, multiple-instance totals, counter resets, log size limits, .NET solution/profile discovery, Next.js script ports, scan limits, authenticated discovery, individual project creation/editing/deletion, duplicate names, failed writes, watch mode persistence and launch argument conversion, and preservation of unrelated settings. Set `PM2_MONITOR_INTEGRATION=1` to additionally verify launch mode changes against a disposable PM2 daemon; it never uses your normal PM2 environment.

Service tests cover installation, executable copying, unit generation, unavailable user managers, custom unit protection, authenticated controls, and disabling autostart without stopping the dashboard. When available, `systemd-analyze verify` checks the generated unit. Set `PM2_MONITOR_SERVICE_INTEGRATION=1` to additionally test HTTP port handover using a temporary executable, configuration, and simulated systemctl; no real service is installed.

The JavaScript tests verify username/password encoding (including Unicode), failed-login prompts, and rejection of previously stored Bearer authorization. They also cover saved group suggestions and filling the project form from discovered targets while preserving the chosen group. Editing and deletion flows are also covered. The tests use a simulated DOM rather than a browser, also verify that referenced controls exist in the HTML, and cover watch mode inference and saving, service status, service handover, and service action errors.

Reference for PM2 application records: [Ecosystem file](https://pm2.keymetrics.io/docs/usage/application-declaration/).
