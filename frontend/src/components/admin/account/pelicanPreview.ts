// This policy is inserted before any model-authored markup. Later policies can
// only restrict it further; the iframe also has a unique opaque origin.
export const PELICAN_PREVIEW_CSP = [
  "default-src 'none'",
  "script-src 'unsafe-inline'",
  "style-src 'unsafe-inline'",
  'img-src data: blob:',
  'font-src data:',
  "connect-src 'none'",
  "frame-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join('; ')

export function pelicanPreviewDocument(html: string, nonce = ''): string {
  // Do not parse or insert generated elements into the admin document, even
  // temporarily: only the sandboxed browsing context may consume this markup.
  const prefix = `<!doctype html><meta http-equiv="Content-Security-Policy" content="${PELICAN_PREVIEW_CSP}">`
  if (!nonce) return prefix + html
  // srcdoc inherits the admin page's nonce-based CSP. Parse and authorize inline
  // script elements only INSIDE the opaque sandbox, after its stricter policy is
  // active. Never relax the admin policy or authorize external scripts here.
  const literal = (value: string) => JSON.stringify(value).replace(/</g, '\\u003c')
  const bootstrap = `(() => { const doc = new DOMParser().parseFromString(${literal(html)}, 'text/html');
for (const script of doc.querySelectorAll('script:not([src])')) script.setAttribute('nonce', ${literal(nonce)});
document.open(); document.write(${literal(prefix)} + doc.documentElement.outerHTML); document.close(); })();`
  const safeNonce = nonce.replace(/[&"<>]/g, character => ({ '&': '&amp;', '"': '&quot;', '<': '&lt;', '>': '&gt;' })[character]!)
  return `${prefix}<script nonce="${safeNonce}">${bootstrap}</script>`
}
