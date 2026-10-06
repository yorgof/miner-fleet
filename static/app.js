'use strict';
document.addEventListener('submit', async event => {
  const form = event.target.closest('.action-form');
  if (!form) return;
  event.preventDefault();
  const saving = form.querySelector('[name="save_profile"]')?.value.trim();
  if (!saving && form.dataset.confirm && !window.confirm(form.dataset.confirm)) return;
  const output = form.querySelector('.action-result');
  const button = form.querySelector('button[type="submit"], button');
  if (output) { output.textContent = 'Working…'; output.className = 'action-result'; }
  if (button) button.disabled = true;
  try {
    const multipart = form.enctype === 'multipart/form-data';
    const body = multipart ? new FormData(form) : new URLSearchParams(new FormData(form));
    const response = await fetch(form.action, {method: 'POST', body});
    if (!response.ok) throw new Error(await response.text());
    if (form.dataset.download) {
      const blob = await response.blob();
      const link = document.createElement('a');
      link.href = URL.createObjectURL(blob);
      const disposition = response.headers.get('Content-Disposition') || '';
      link.download = disposition.match(/filename="([^"]+)"/)?.[1] || 'miner-data.txt';
      link.click();
      setTimeout(() => URL.revokeObjectURL(link.href), 1000);
      if (output) output.textContent = 'Download ready.';
    } else {
      if (output) { output.textContent = await response.text(); output.classList.add('good'); }
      form.querySelectorAll('input[type="password"], input[name="otp"]').forEach(input => {input.value = '';});
    }
  } catch (error) {
    if (output) { output.textContent = error.message; output.classList.add('stat-error'); }
  } finally {
    if (button) button.disabled = false;
  }
});
document.addEventListener('input', event => {
  if (!event.target.matches('.data-filter')) return;
  const query = event.target.value.toLowerCase();
  const panel = event.target.closest('.panel');
  panel.querySelectorAll('.telemetry tr').forEach(row => {row.hidden = !row.textContent.toLowerCase().includes(query);});
  panel.querySelectorAll('.data-group').forEach(group => {group.hidden = !group.querySelector('tr:not([hidden])');});
});

// Preserve telemetry filters and expanded groups across HTMX polling swaps.
let telemetryView = null;
document.addEventListener('htmx:beforeSwap', event => {
 const target = event.detail.target;
 if (!target?.querySelector('.data-filter')) return;
 telemetryView = {query:target.querySelector('.data-filter').value,
 groups:new Map([...target.querySelectorAll('.data-group')].map(group=>[group.querySelector('summary').textContent,group.open]))};
});
document.addEventListener('htmx:afterSwap', event => {
 const target = document.getElementById('miner-telemetry');
 const filter = target?.querySelector('.data-filter');
 if (!filter || !telemetryView) return;
 filter.value = telemetryView.query;
 target.querySelectorAll('.data-group').forEach(group=>{
 const open = telemetryView.groups.get(group.querySelector('summary').textContent);
 if (open !== undefined) group.open = open;
 });
 filter.dispatchEvent(new Event('input',{bubbles:true}));
});
