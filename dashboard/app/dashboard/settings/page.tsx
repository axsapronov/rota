"use client"

import * as React from "react"
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "@/components/ui/card"
import { Label } from "@/components/ui/label"
import { Input } from "@/components/ui/input"
import { Button } from "@/components/ui/button"
import { Switch } from "@/components/ui/switch"
import { Textarea } from "@/components/ui/textarea"
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import {
  Shield,
  RotateCw,
  Gauge,
  Activity,
  Save,
  Loader2,
  Database,
  ChevronDown,
  KeyRound,
  Eye,
  EyeOff,
  Globe,
  Download,
} from "lucide-react"
import { api } from "@/lib/api"
import { Settings } from "@/lib/types"
import { toast } from "sonner"

export default function SettingsPage() {
  const [settings, setSettings] = React.useState<Settings | null>(null)
  const [isLoading, setIsLoading] = React.useState(true)
  const [isSaving, setIsSaving] = React.useState(false)

  // Admin account state
  const [adminUsername, setAdminUsername] = React.useState("")
  const [newUsername, setNewUsername] = React.useState("")
  const [currentPass, setCurrentPass] = React.useState("")
  const [newPass, setNewPass] = React.useState("")
  const [confirmPass, setConfirmPass] = React.useState("")
  const [showPass, setShowPass] = React.useState(false)
  const [changingPass, setChangingPass] = React.useState(false)
  const [isUpdatingGeoDB, setIsUpdatingGeoDB] = React.useState(false)

  const handleUpdateGeoDB = async () => {
    try {
      setIsUpdatingGeoDB(true)
      const res = await api.updateGeoIPDB()
      toast.success(res.message || "MaxMind GeoIP database updated successfully")
      const updated = await api.getSettings()
      setSettings(updated)
    } catch (error) {
      toast.error(error instanceof Error ? error.message : "Failed to update MaxMind GeoIP DB")
    } finally {
      setIsUpdatingGeoDB(false)
    }
  }

  React.useEffect(() => {
    const fetchSettings = async () => {
      try {
        const [data, adminInfo] = await Promise.all([
          api.getSettings(),
          api.getAdminInfo(),
        ])
        setSettings(data)
        setAdminUsername(adminInfo.username)
        setNewUsername(adminInfo.username)
      } catch (error) {
        console.error("Failed to fetch settings:", error)
      } finally {
        setIsLoading(false)
      }
    }

    fetchSettings()
  }, [])

  const handleChangePassword = async () => {
    if (!currentPass) { toast.error("Enter your current password"); return }
    if (!newPass) { toast.error("Enter a new password"); return }
    if (newPass.length < 6) { toast.error("New password must be at least 6 characters"); return }
    if (newPass !== confirmPass) { toast.error("Passwords don't match"); return }

    setChangingPass(true)
    try {
      const opts: { current_password: string; new_password: string; new_username?: string } = {
        current_password: currentPass,
        new_password: newPass,
      }
      if (newUsername && newUsername !== adminUsername) {
        opts.new_username = newUsername
      }
      const res = await api.changePassword(opts)
      setAdminUsername(res.username)
      setNewUsername(res.username)
      setCurrentPass("")
      setNewPass("")
      setConfirmPass("")
      toast.success("Credentials updated successfully")
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Failed to change password")
    } finally {
      setChangingPass(false)
    }
  }

  const handleSave = async () => {
    if (!settings) return

    try {
      setIsSaving(true)
      await api.updateSettings(settings)
      toast.success("Settings saved successfully")
    } catch (error) {
      console.error("Failed to save settings:", error)
      toast.error("Failed to save settings")
    } finally {
      setIsSaving(false)
    }
  }

  const handleReset = async () => {
    if (!confirm("Are you sure you want to reset all settings to defaults?")) return

    try {
      setIsSaving(true)
      const response = await api.resetSettings()
      setSettings(response.config)
      toast.success("Settings reset to defaults")
    } catch (error) {
      console.error("Failed to reset settings:", error)
      toast.error("Failed to reset settings")
    } finally {
      setIsSaving(false)
    }
  }

  if (isLoading || !settings) {
    return (
      <div className="flex items-center justify-center h-96">
        <Loader2 className="h-8 w-8 animate-spin" />
      </div>
    )
  }

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between">
        <div>
          <h1 className="text-3xl font-bold tracking-tight">Settings</h1>
          <p className="text-muted-foreground">
            Configure your Rota proxy rotation system
          </p>
        </div>
        <div className="flex gap-2">
          <Button variant="outline" size="lg" onClick={handleReset} disabled={isSaving}>
            Reset to Defaults
          </Button>
          <Button size="lg" onClick={handleSave} disabled={isSaving}>
            {isSaving ? (
              <>
                <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                Saving...
              </>
            ) : (
              <>
                <Save className="mr-2 h-4 w-4" />
                Save Configuration
              </>
            )}
          </Button>
        </div>
      </div>

      {/* Admin Account */}
      <Card>
        <CardHeader>
          <div className="flex items-center gap-2">
            <KeyRound className="h-5 w-5" />
            <CardTitle>Admin Account</CardTitle>
          </div>
          <CardDescription>
            Change the dashboard login credentials. Current user: <strong>{adminUsername}</strong>
          </CardDescription>
        </CardHeader>
        <CardContent>
          <div className="grid gap-4 max-w-md">
            <div className="space-y-1.5">
              <Label>Username</Label>
              <Input
                value={newUsername}
                onChange={e => setNewUsername(e.target.value)}
                placeholder="New username (leave unchanged to keep)"
              />
            </div>
            <div className="space-y-1.5">
              <Label>Current password <span className="text-destructive">*</span></Label>
              <div className="relative">
                <Input
                  type={showPass ? "text" : "password"}
                  value={currentPass}
                  onChange={e => setCurrentPass(e.target.value)}
                  placeholder="Required to confirm any change"
                  className="pr-10"
                />
                <button
                  type="button"
                  className="absolute right-3 top-1/2 -translate-y-1/2 text-muted-foreground hover:text-foreground"
                  onClick={() => setShowPass(v => !v)}
                >
                  {showPass ? <EyeOff className="h-4 w-4" /> : <Eye className="h-4 w-4" />}
                </button>
              </div>
            </div>
            <div className="space-y-1.5">
              <Label>New password</Label>
              <Input
                type={showPass ? "text" : "password"}
                value={newPass}
                onChange={e => setNewPass(e.target.value)}
                placeholder="Min 6 characters"
              />
            </div>
            <div className="space-y-1.5">
              <Label>Confirm new password</Label>
              <Input
                type={showPass ? "text" : "password"}
                value={confirmPass}
                onChange={e => setConfirmPass(e.target.value)}
                placeholder="Repeat new password"
              />
            </div>
            <Button
              onClick={handleChangePassword}
              disabled={changingPass || !currentPass || !newPass}
              className="w-fit"
            >
              {changingPass
                ? <><Loader2 className="mr-2 h-4 w-4 animate-spin" />Saving…</>
                : <><KeyRound className="mr-2 h-4 w-4" />Update credentials</>}
            </Button>
          </div>
        </CardContent>
      </Card>

      {/* Proxy Rotation Settings - Full Width (Most Important) */}
      <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <RotateCw className="h-5 w-5" />
              <CardTitle>Proxy Rotation</CardTitle>
            </div>
            <CardDescription>
              Configure proxy rotation strategy and behavior
            </CardDescription>
          </CardHeader>
          <CardContent>
            <div className="grid gap-6 md:grid-cols-2">
              {/* Left Column */}
              <div className="space-y-4">
                <div className="space-y-2">
                  <Label htmlFor="rotation-method">Rotation Method</Label>
                  <Select
                    value={settings.rotation.method}
                    onValueChange={(value: string) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, method: value as Settings["rotation"]["method"] },
                      })
                    }
                  >
                    <SelectTrigger id="rotation-method">
                      <SelectValue placeholder="Select method" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="random">Random</SelectItem>
                      <SelectItem value="roundrobin">Round Robin</SelectItem>
                      <SelectItem value="least_conn">Least Connections</SelectItem>
                      <SelectItem value="time_based">Time Based</SelectItem>
                    </SelectContent>
                  </Select>
                </div>

                {settings.rotation.method === "time_based" && (
                  <div className="space-y-2">
                    <Label htmlFor="rotation-interval">Time Based Interval (seconds)</Label>
                    <Input
                      id="rotation-interval"
                      type="number"
                      value={settings.rotation.time_based?.interval || 120}
                      onChange={(e) =>
                        setSettings({
                          ...settings,
                          rotation: {
                            ...settings.rotation,
                            time_based: { interval: parseInt(e.target.value) },
                          },
                        })
                      }
                    />
                  </div>
                )}

                <div className="space-y-2">
                  <Label htmlFor="rotation-timeout">Timeout (seconds)</Label>
                  <Input
                    id="rotation-timeout"
                    type="number"
                    value={settings.rotation.timeout}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, timeout: parseInt(e.target.value) },
                      })
                    }
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="rotation-retries">Retries</Label>
                  <Input
                    id="rotation-retries"
                    type="number"
                    value={settings.rotation.retries}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, retries: parseInt(e.target.value) },
                      })
                    }
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="fallback-retries">Fallback Max Retries</Label>
                  <Input
                    id="fallback-retries"
                    type="number"
                    value={settings.rotation.fallback_max_retries}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, fallback_max_retries: parseInt(e.target.value) },
                      })
                    }
                  />
                </div>

                <div className="space-y-2">
                  <Label htmlFor="max-response-time">Max Response Time (ms)</Label>
                  <Input
                    id="max-response-time"
                    type="number"
                    value={settings.rotation.max_response_time || 0}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, max_response_time: parseInt(e.target.value) || 0 },
                      })
                    }
                  />
                  <p className="text-xs text-muted-foreground">
                    0 means no limit. Only use proxies faster than this.
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="min-success-rate">Min Success Rate (%)</Label>
                  <Input
                    id="min-success-rate"
                    type="number"
                    min="0"
                    max="100"
                    step="1"
                    value={settings.rotation.min_success_rate || 0}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, min_success_rate: parseFloat(e.target.value) || 0 },
                      })
                    }
                  />
                  <p className="text-xs text-muted-foreground">
                    0 means no minimum. Only use proxies with success rate above this.
                  </p>
                </div>
              </div>

              {/* Right Column */}
              <div className="space-y-4">
                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label htmlFor="remove-unhealthy">Remove Unhealthy</Label>
                    <p className="text-xs text-muted-foreground">
                      Remove unhealthy proxies from rotation
                    </p>
                  </div>
                  <Switch
                    id="remove-unhealthy"
                    checked={settings.rotation.remove_unhealthy}
                    onCheckedChange={(checked) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, remove_unhealthy: checked },
                      })
                    }
                  />
                </div>

                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label htmlFor="fallback">Enable Fallback</Label>
                    <p className="text-xs text-muted-foreground">
                      Continuous operation in case of failures
                    </p>
                  </div>
                  <Switch
                    id="fallback"
                    checked={settings.rotation.fallback}
                    onCheckedChange={(checked) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, fallback: checked },
                      })
                    }
                  />
                </div>

                <div className="flex items-center justify-between">
                  <div className="space-y-0.5">
                    <Label htmlFor="follow-redirect">Follow Redirect</Label>
                    <p className="text-xs text-muted-foreground">
                      Follow HTTP redirections
                    </p>
                  </div>
                  <Switch
                    id="follow-redirect"
                    checked={settings.rotation.follow_redirect}
                    onCheckedChange={(checked) =>
                      setSettings({
                        ...settings,
                        rotation: { ...settings.rotation, follow_redirect: checked },
                      })
                    }
                  />
                </div>

                <div className="space-y-2">
                  <Label>Allowed Protocols</Label>
                  <DropdownMenu>
                    <DropdownMenuTrigger asChild>
                      <Button variant="outline" className="w-full justify-between">
                        <span>
                          {settings.rotation.allowed_protocols?.length === 5
                            ? "All Protocols"
                            : settings.rotation.allowed_protocols?.length > 0
                            ? `${settings.rotation.allowed_protocols.length} selected`
                            : "Select protocols"}
                        </span>
                        <ChevronDown className="ml-2 h-4 w-4" />
                      </Button>
                    </DropdownMenuTrigger>
                    <DropdownMenuContent className="w-56">
                      <DropdownMenuLabel>Select Protocols</DropdownMenuLabel>
                      <DropdownMenuSeparator />
                      {["http", "https", "socks4", "socks4a", "socks5"].map((protocol) => (
                        <DropdownMenuCheckboxItem
                          key={protocol}
                          checked={settings.rotation.allowed_protocols?.includes(protocol)}
                          onCheckedChange={(checked) => {
                            const current = settings.rotation.allowed_protocols || [];
                            const updated = checked
                              ? [...current, protocol]
                              : current.filter(p => p !== protocol);
                            setSettings({
                              ...settings,
                              rotation: { ...settings.rotation, allowed_protocols: updated },
                            });
                          }}
                        >
                          {protocol.toUpperCase()}
                        </DropdownMenuCheckboxItem>
                      ))}
                    </DropdownMenuContent>
                  </DropdownMenu>
                  <p className="text-xs text-muted-foreground">
                    Select which protocols to use for proxy rotation
                  </p>
                </div>
              </div>
            </div>
          </CardContent>
        </Card>

      {/* Other Settings in 2-column grid */}
      <div className="grid gap-4 md:grid-cols-2">
        {/* Authentication Settings */}
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Shield className="h-5 w-5" />
              <CardTitle>Authentication</CardTitle>
            </div>
            <CardDescription>
              Basic authentication settings for your proxy server
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label htmlFor="auth-enabled">Enable Authentication</Label>
                <p className="text-xs text-muted-foreground">
                  Require username and password for proxy connections
                </p>
              </div>
              <Switch
                id="auth-enabled"
                checked={settings.authentication.enabled}
                onCheckedChange={(checked) =>
                  setSettings({
                    ...settings,
                    authentication: { ...settings.authentication, enabled: checked },
                  })
                }
              />
            </div>
            {settings.authentication.enabled && (
              <>
                <div className="space-y-2">
                  <Label htmlFor="auth-username">Username</Label>
                  <Input
                    id="auth-username"
                    type="text"
                    value={settings.authentication.username}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        authentication: { ...settings.authentication, username: e.target.value },
                      })
                    }
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="auth-password">Password</Label>
                  <Input
                    id="auth-password"
                    type="password"
                    placeholder="Enter new password"
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        authentication: { ...settings.authentication, password: e.target.value },
                      })
                    }
                  />
                  <p className="text-xs text-muted-foreground">
                    Leave empty to keep current password
                  </p>
                </div>
              </>
            )}
          </CardContent>
        </Card>

        {/* Rate Limit Settings */}
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Gauge className="h-5 w-5" />
              <CardTitle>Rate Limiting</CardTitle>
            </div>
            <CardDescription>
              Control request rate limits
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label htmlFor="rate-limit-enabled">Enable Rate Limiting</Label>
                <p className="text-xs text-muted-foreground">
                  Limit number of requests per interval
                </p>
              </div>
              <Switch
                id="rate-limit-enabled"
                checked={settings.rate_limit.enabled}
                onCheckedChange={(checked) =>
                  setSettings({
                    ...settings,
                    rate_limit: { ...settings.rate_limit, enabled: checked },
                  })
                }
              />
            </div>

            {settings.rate_limit.enabled && (
              <>
                <div className="space-y-2">
                  <Label htmlFor="rate-limit-interval">Interval (seconds)</Label>
                  <Input
                    id="rate-limit-interval"
                    type="number"
                    value={settings.rate_limit.interval}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rate_limit: { ...settings.rate_limit, interval: parseInt(e.target.value) },
                      })
                    }
                  />
                </div>
                <div className="space-y-2">
                  <Label htmlFor="rate-limit-max">Max Requests per Interval</Label>
                  <Input
                    id="rate-limit-max"
                    type="number"
                    value={settings.rate_limit.max_requests}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        rate_limit: { ...settings.rate_limit, max_requests: parseInt(e.target.value) },
                      })
                    }
                  />
                </div>
              </>
            )}
          </CardContent>
        </Card>

        {/* Health Check Settings */}
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Activity className="h-5 w-5" />
              <CardTitle>Health Check</CardTitle>
            </div>
            <CardDescription>
              Configure proxy health monitoring
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="healthcheck-timeout">Timeout (seconds)</Label>
              <Input
                id="healthcheck-timeout"
                type="number"
                value={settings.healthcheck.timeout}
                onChange={(e) =>
                  setSettings({
                    ...settings,
                    healthcheck: { ...settings.healthcheck, timeout: parseInt(e.target.value) },
                  })
                }
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="healthcheck-workers">Number of Workers</Label>
              <Input
                id="healthcheck-workers"
                type="number"
                value={settings.healthcheck.workers}
                onChange={(e) =>
                  setSettings({
                    ...settings,
                    healthcheck: { ...settings.healthcheck, workers: parseInt(e.target.value) },
                  })
                }
              />
              <p className="text-xs text-muted-foreground">
                Number of concurrent workers to check proxies
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="healthcheck-url">Health Check URL</Label>
              <Input
                id="healthcheck-url"
                type="url"
                value={settings.healthcheck.url}
                onChange={(e) =>
                  setSettings({
                    ...settings,
                    healthcheck: { ...settings.healthcheck, url: e.target.value },
                  })
                }
              />
              <p className="text-xs text-muted-foreground">
                Only GET method is supported
              </p>
            </div>

            <div className="space-y-2">
              <Label htmlFor="healthcheck-status">Expected Status Code</Label>
              <Input
                id="healthcheck-status"
                type="number"
                value={settings.healthcheck.status}
                onChange={(e) =>
                  setSettings({
                    ...settings,
                    healthcheck: { ...settings.healthcheck, status: parseInt(e.target.value) },
                  })
                }
              />
            </div>

            <div className="space-y-2">
              <Label htmlFor="healthcheck-headers">Headers</Label>
              <Textarea
                id="healthcheck-headers"
                placeholder="Content-Type: application/json&#10;User-Agent: Rota/1.0"
                value={settings.healthcheck.headers.join("\n")}
                onChange={(e) =>
                  setSettings({
                    ...settings,
                    healthcheck: {
                      ...settings.healthcheck,
                      headers: e.target.value.split("\n").filter((h) => h.trim()),
                    },
                  })
                }
                rows={4}
                className="font-mono text-sm"
              />
              <p className="text-xs text-muted-foreground">
                One header per line in format: Key: Value
              </p>
            </div>

            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label htmlFor="healthcheck-strict-tls">Strict TLS Validation</Label>
                <p className="text-xs text-muted-foreground">
                  Reject proxies with expired or invalid certificates
                </p>
              </div>
              <Switch
                id="healthcheck-strict-tls"
                checked={settings.healthcheck.strict_tls ?? false}
                onCheckedChange={(checked) =>
                  setSettings({
                    ...settings,
                    healthcheck: { ...settings.healthcheck, strict_tls: checked },
                  })
                }
              />
            </div>
          </CardContent>
        </Card>

        {/* Global Health Check Settings */}
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Activity className="h-5 w-5" />
              <CardTitle>Global Health Check</CardTitle>
            </div>
            <CardDescription>
              Periodically check orphan proxies (not attached to any pool)
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label htmlFor="global-healthcheck-enabled">Enable Global Health Check</Label>
                <p className="text-xs text-muted-foreground">
                  Schedule health checks for orphan proxies
                </p>
              </div>
              <Switch
                id="global-healthcheck-enabled"
                checked={settings.global_health_check?.enabled ?? true}
                onCheckedChange={(checked) =>
                  setSettings({
                    ...settings,
                    global_health_check: {
                      ...settings.global_health_check,
                      enabled: checked,
                    },
                  })
                }
              />
            </div>

            {(settings.global_health_check?.enabled ?? true) && (
              <div className="space-y-2">
                <Label htmlFor="global-healthcheck-interval">Interval (minutes)</Label>
                <Input
                  id="global-healthcheck-interval"
                  type="number"
                  min={1}
                  max={1440}
                  value={settings.global_health_check?.interval_minutes ?? 30}
                  onChange={(e) =>
                    setSettings({
                      ...settings,
                      global_health_check: {
                        ...settings.global_health_check,
                        interval_minutes: parseInt(e.target.value) || 30,
                      },
                    })
                  }
                />
                <p className="text-xs text-muted-foreground">
                  How often to run the orphan proxy health check (1–1440 minutes)
                </p>
              </div>
            )}
          </CardContent>
        </Card>

        {/* Log Retention Settings */}
        <Card>
          <CardHeader>
            <div className="flex items-center gap-2">
              <Database className="h-5 w-5" />
              <CardTitle>Log Retention</CardTitle>
            </div>
            <CardDescription>
              Configure automatic proxy log cleanup and compression
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="flex items-center justify-between">
              <div className="space-y-0.5">
                <Label htmlFor="log-retention-enabled">Enable Auto Cleanup</Label>
                <p className="text-xs text-muted-foreground">
                  Automatically delete old logs based on retention policy
                </p>
              </div>
              <Switch
                id="log-retention-enabled"
                checked={settings.log_retention?.enabled ?? true}
                onCheckedChange={(checked) =>
                  setSettings({
                    ...settings,
                    log_retention: { ...settings.log_retention, enabled: checked },
                  })
                }
              />
            </div>

            {settings.log_retention?.enabled && (
              <>
                <div className="space-y-2">
                  <Label htmlFor="retention-days">Retention Period</Label>
                  <Select
                    value={settings.log_retention.retention_days?.toString() || "30"}
                    onValueChange={(value) =>
                      setSettings({
                        ...settings,
                        log_retention: {
                          ...settings.log_retention,
                          retention_days: parseInt(value),
                        },
                      })
                    }
                  >
                    <SelectTrigger id="retention-days">
                      <SelectValue placeholder="Select retention period" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="7">7 days</SelectItem>
                      <SelectItem value="15">15 days</SelectItem>
                      <SelectItem value="30">30 days (Recommended)</SelectItem>
                      <SelectItem value="60">60 days</SelectItem>
                      <SelectItem value="90">90 days</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    Proxy logs older than this will be permanently deleted
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="compression-days">Compression After</Label>
                  <Select
                    value={settings.log_retention.compression_after_days?.toString() || "7"}
                    onValueChange={(value) =>
                      setSettings({
                        ...settings,
                        log_retention: {
                          ...settings.log_retention,
                          compression_after_days: parseInt(value),
                        },
                      })
                    }
                  >
                    <SelectTrigger id="compression-days">
                      <SelectValue placeholder="Select compression period" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="1">1 day</SelectItem>
                      <SelectItem value="3">3 days</SelectItem>
                      <SelectItem value="7">7 days (Recommended)</SelectItem>
                      <SelectItem value="14">14 days</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    Logs older than this will be compressed to save space
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="cleanup-interval">Cleanup Interval</Label>
                  <Select
                    value={settings.log_retention.cleanup_interval_hours?.toString() || "24"}
                    onValueChange={(value) =>
                      setSettings({
                        ...settings,
                        log_retention: {
                          ...settings.log_retention,
                          cleanup_interval_hours: parseInt(value),
                        },
                      })
                    }
                  >
                    <SelectTrigger id="cleanup-interval">
                      <SelectValue placeholder="Select cleanup interval" />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value="1">Every 1 hour</SelectItem>
                      <SelectItem value="6">Every 6 hours</SelectItem>
                      <SelectItem value="12">Every 12 hours</SelectItem>
                      <SelectItem value="24">Every 24 hours (Recommended)</SelectItem>
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground">
                    How often to run the cleanup job
                  </p>
                </div>

                <div className="rounded-lg bg-muted p-3 text-sm">
                  <p className="font-medium mb-1">Current Configuration:</p>
                  <ul className="space-y-1 text-muted-foreground">
                    <li>• Logs kept for {settings.log_retention.retention_days} days</li>
                    <li>• Compressed after {settings.log_retention.compression_after_days} days</li>
                    <li>• Cleanup runs every {settings.log_retention.cleanup_interval_hours} hours</li>
                  </ul>
                </div>
              </>
            )}
          </CardContent>
        </Card>

        {/* GeoIP Configuration */}
        <Card className="md:col-span-2">
          <CardHeader>
            <div className="flex items-center justify-between">
              <div className="flex items-center gap-2">
                <Globe className="h-5 w-5" />
                <CardTitle>GeoIP Configuration</CardTitle>
              </div>
              {settings.geoip?.provider === "maxmind" && (
                <Button
                  variant="outline"
                  size="sm"
                  onClick={handleUpdateGeoDB}
                  disabled={isUpdatingGeoDB || isSaving}
                >
                  {isUpdatingGeoDB ? (
                    <>
                      <Loader2 className="mr-2 h-4 w-4 animate-spin" />
                      Downloading DB...
                    </>
                  ) : (
                    <>
                      <Download className="mr-2 h-4 w-4" />
                      Update DB Now
                    </>
                  )}
                </Button>
              )}
            </div>
            <CardDescription>
              Select geolocation lookup database provider (ip-api.com or MaxMind GeoIP DB)
            </CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="geoip-provider">GeoIP Provider</Label>
              <Select
                value={settings.geoip?.provider || "ip-api"}
                onValueChange={(value: "ip-api" | "maxmind") =>
                  setSettings({
                    ...settings,
                    geoip: {
                      provider: value,
                      maxmind_license_key: settings.geoip?.maxmind_license_key || "",
                      maxmind_db_path: settings.geoip?.maxmind_db_path || "data/GeoLite2-City.mmdb",
                      maxmind_url: settings.geoip?.maxmind_url || "",
                      auto_update: settings.geoip?.auto_update ?? false,
                      update_interval_hours: settings.geoip?.update_interval_hours || 168,
                      last_updated_at: settings.geoip?.last_updated_at,
                    },
                  })
                }
              >
                <SelectTrigger id="geoip-provider">
                  <SelectValue placeholder="Select provider" />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="ip-api">ip-api.com (Free Web API)</SelectItem>
                  <SelectItem value="maxmind">MaxMind GeoIP DB (Local MMDB)</SelectItem>
                </SelectContent>
              </Select>
              <p className="text-xs text-muted-foreground">
                {settings.geoip?.provider === "maxmind"
                  ? "Uses a local MaxMind GeoIP database file for instant, rate-limit-free lookups."
                  : "Uses ip-api.com batch API endpoint (rate limited for free tier)."}
              </p>
            </div>

            {settings.geoip?.provider === "maxmind" && (
              <div className="grid gap-4 md:grid-cols-2 pt-2 border-t">
                <div className="space-y-2">
                  <Label htmlFor="maxmind-key">MaxMind License Key</Label>
                  <Input
                    id="maxmind-key"
                    type={showPass ? "text" : "password"}
                    value={settings.geoip?.maxmind_license_key || ""}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        geoip: { ...settings.geoip, maxmind_license_key: e.target.value },
                      })
                    }
                    placeholder="Enter MaxMind account license key"
                  />
                  <p className="text-xs text-muted-foreground">
                    Required to download official GeoLite2-City database automatically.
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="maxmind-path">Database Storage Path</Label>
                  <Input
                    id="maxmind-path"
                    type="text"
                    value={settings.geoip?.maxmind_db_path || "data/GeoLite2-City.mmdb"}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        geoip: { ...settings.geoip, maxmind_db_path: e.target.value },
                      })
                    }
                    placeholder="data/GeoLite2-City.mmdb"
                  />
                  <p className="text-xs text-muted-foreground">
                    Path where `.mmdb` file is stored locally.
                  </p>
                </div>

                <div className="space-y-2">
                  <Label htmlFor="maxmind-url">Download URL</Label>
                  <Input
                    id="maxmind-url"
                    type="url"
                    value={settings.geoip?.maxmind_url ?? "https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-City.mmdb"}
                    onChange={(e) =>
                      setSettings({
                        ...settings,
                        geoip: { ...settings.geoip, maxmind_url: e.target.value },
                      })
                    }
                    placeholder="https://raw.githubusercontent.com/P3TERX/GeoLite.mmdb/download/GeoLite2-City.mmdb"
                  />
                  <p className="text-xs text-muted-foreground">
                    Defaults to P3TERX daily GeoLite2-City mirror if License Key is omitted.
                  </p>
                </div>

                <div className="space-y-4">
                  <div className="flex items-center justify-between">
                    <div className="space-y-0.5">
                      <Label htmlFor="geoip-autoupdate">Auto-update Database</Label>
                      <p className="text-xs text-muted-foreground">
                        Automatically update database on interval
                      </p>
                    </div>
                    <Switch
                      id="geoip-autoupdate"
                      checked={settings.geoip?.auto_update ?? false}
                      onCheckedChange={(checked) =>
                        setSettings({
                          ...settings,
                          geoip: { ...settings.geoip, auto_update: checked },
                        })
                      }
                    />
                  </div>

                  {settings.geoip?.auto_update && (
                    <div className="space-y-2">
                      <Label htmlFor="update-interval">Update Interval (hours)</Label>
                      <Input
                        id="update-interval"
                        type="number"
                        min="1"
                        value={settings.geoip?.update_interval_hours || 168}
                        onChange={(e) =>
                          setSettings({
                            ...settings,
                            geoip: {
                              ...settings.geoip,
                              update_interval_hours: parseInt(e.target.value) || 168,
                            },
                          })
                        }
                      />
                      <p className="text-xs text-muted-foreground">
                        Recommended: 168 hours (7 days)
                      </p>
                    </div>
                  )}
                </div>

                {settings.geoip?.last_updated_at && (
                  <div className="md:col-span-2 rounded-lg bg-muted p-3 text-xs text-muted-foreground">
                    Last updated: {new Date(settings.geoip.last_updated_at).toLocaleString()}
                  </div>
                )}
              </div>
            )}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
