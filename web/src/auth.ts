import './styles/base.css';
import './styles/auth.css';

import { initializeTheme } from './theme';

interface ChallengeResponse {
  challenge_id: string;
  challenge: string;
}

interface ErrorResponse {
  error?: string;
  error_description?: string;
}

function requiredElement<T extends HTMLElement>(
  selector: string,
  constructor: { new (): T },
): T {
  const element = document.querySelector(selector);
  if (!(element instanceof constructor)) {
    throw new Error(`Required element is missing: ${selector}`);
  }
  return element;
}

function decodeBase64(value: string): Uint8Array<ArrayBuffer> {
  const normalized = value.replace(/\s/g, '').replace(/-/g, '+').replace(/_/g, '/');
  const padded = normalized + '='.repeat((4 - normalized.length % 4) % 4);
  return Uint8Array.from(atob(padded), (character) => character.charCodeAt(0));
}

function encodeBase64(value: ArrayBuffer): string {
  let binary = '';
  for (const byte of new Uint8Array(value)) {
    binary += String.fromCharCode(byte);
  }
  return btoa(binary).replace(/=+$/, '');
}

function challengeMessage(challenge: Uint8Array<ArrayBuffer>): Uint8Array<ArrayBuffer> {
  const context = new TextEncoder().encode('slider-web-auth-v1\0');
  const message = new Uint8Array(context.length + challenge.length);
  message.set(context);
  message.set(challenge, context.length);
  return message;
}

async function errorMessage(response: Response, fallback: string): Promise<string> {
  try {
    const error = await response.json() as ErrorResponse;
    return error.error_description || error.error || fallback;
  } catch {
    return fallback;
  }
}

initializeTheme();

const form = requiredElement('#loginForm', HTMLFormElement);
const fingerprintInput = requiredElement('#fingerprint', HTMLInputElement);
const privateKeyInput = requiredElement('#privateKey', HTMLInputElement);
const submitButton = requiredElement('#submitBtn', HTMLButtonElement);
const errorElement = requiredElement('#error', HTMLDivElement);
const authPath = document.body.dataset.authPath || '/auth';
const authLoginPath = document.body.dataset.authLoginPath || `${authPath}/token`;
const consolePath = document.body.dataset.consolePath || '/console';

function showError(message: string): void {
  errorElement.textContent = message;
  errorElement.classList.add('show');
}

function hideError(): void {
  errorElement.classList.remove('show');
}

form.addEventListener('submit', async (event) => {
  event.preventDefault();
  hideError();

  const fingerprint = fingerprintInput.value.trim();
  const privateKey = privateKeyInput.value.trim();
  if (!fingerprint || !privateKey) {
    showError('Please enter the certificate fingerprint and private key');
    return;
  }

  submitButton.disabled = true;
  submitButton.textContent = 'Authenticating...';

  try {
    const challengeResponse = await fetch(`${authPath}/challenge`, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({ fingerprint }),
      credentials: 'same-origin',
    });
    if (!challengeResponse.ok) {
      throw new Error(await errorMessage(challengeResponse, 'Challenge request failed'));
    }

    const challenge = await challengeResponse.json() as ChallengeResponse;
    const signingKey = await crypto.subtle.importKey(
      'pkcs8',
      decodeBase64(privateKey),
      { name: 'Ed25519' },
      false,
      ['sign'],
    );
    const signature = await crypto.subtle.sign(
      'Ed25519',
      signingKey,
      challengeMessage(decodeBase64(challenge.challenge)),
    );
    privateKeyInput.value = '';

    const tokenResponse = await fetch(authLoginPath, {
      method: 'POST',
      headers: {
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        fingerprint,
        challenge_id: challenge.challenge_id,
        signature: encodeBase64(signature),
      }),
      credentials: 'same-origin',
    });
    if (!tokenResponse.ok) {
      throw new Error(await errorMessage(tokenResponse, 'Authentication failed'));
    }

    window.location.assign(consolePath);
  } catch (error) {
    showError(error instanceof Error ? error.message : 'Authentication failed');
    submitButton.disabled = false;
    submitButton.textContent = 'Authenticate';
  }
});
