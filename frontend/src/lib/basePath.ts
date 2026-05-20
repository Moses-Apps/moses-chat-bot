// The runtime deploy prefix Moses serves this app under, e.g.
// "/apps/<tenant>/<app>". The nginx entrypoint injects it into
// <meta name="moses-base-path"> from the MOSES_BASE_PATH env. This is NOT the
// same as the build-time vite base (which is './') — anything that must
// resolve against the real deployed URL (the React Router basename, the bot
// backend API base) has to use this, not import.meta.env.BASE_URL.
//
// Returns "" (root) when the tag is absent — standalone dev / jsdom. Never
// has a trailing slash.
export function mosesBasePath(): string {
  const content = document
    .querySelector('meta[name="moses-base-path"]')
    ?.getAttribute('content')
    ?.trim();
  if (content && content.startsWith('/')) {
    return content.replace(/\/+$/, '');
  }
  return '';
}
