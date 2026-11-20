export function installCommand(version: string, args: string) {
  const tag = /^v[0-9]+\.[0-9]+\.[0-9]+(?:-[A-Za-z0-9]+(?:\.[A-Za-z0-9]+)*)?$/.test(version) ? version : ''
  const script = `https://raw.githubusercontent.com/revfanc/boxpanel/${tag || 'main'}/scripts/install.sh`
  return `bash <(curl -fsSL ${script}) ${args}` + (tag ? ` --version ${tag}` : '')
}
