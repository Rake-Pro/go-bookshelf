/**
 * Politeness-controlled screen reader announcements.
 *
 * index.html carries two permanent live regions (`#live-polite`, `#live-assertive`).
 * Repeated identical strings are re-announced by clearing first, otherwise some
 * screen readers stay silent.
 */

/** @param {string} id @param {string} message */
function say(id, message) {
  const el = document.getElementById(id);
  if (!el || !message) return;
  el.textContent = '';
  // A separate task is required for the change to be picked up reliably.
  setTimeout(() => { el.textContent = message; }, 40);
}

/** @param {string} message */
export const announce = (message) => say('live-polite', message);

/** @param {string} message */
export const alert = (message) => say('live-assertive', message);
