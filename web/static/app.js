// Progressive enhancements only; every feature works without JavaScript
// except the confirmation prompts and print buttons.
(function () {
  "use strict";

  // Mobile navigation toggle
  document.querySelectorAll("[data-nav-toggle]").forEach(function (btn) {
    btn.addEventListener("click", function () {
      var nav = document.getElementById("nav");
      var open = nav.classList.toggle("open");
      btn.setAttribute("aria-expanded", open ? "true" : "false");
    });
  });

  // Confirmation for destructive / irreversible actions
  document.addEventListener("submit", function (e) {
    var form = e.target;
    var msg = form.getAttribute("data-confirm");
    if (!msg && e.submitter) msg = e.submitter.getAttribute("data-confirm");
    if (msg && !window.confirm(msg)) {
      e.preventDefault();
      return;
    }
    // Prevent accidental double submission
    if (form.dataset.submitted === "1") {
      e.preventDefault();
      return;
    }
    if ((form.method || "").toLowerCase() === "post") {
      form.dataset.submitted = "1";
      setTimeout(function () { form.dataset.submitted = ""; }, 4000);
    }
  });

  // Print buttons
  document.querySelectorAll("[data-print]").forEach(function (b) {
    b.addEventListener("click", function () { window.print(); });
  });
  if (document.body.hasAttribute("data-autoprint") && location.hash === "#print") {
    window.addEventListener("load", function () { window.print(); });
  }

  // Auto-submit filter selects
  document.querySelectorAll("select[data-autosubmit]").forEach(function (s) {
    s.addEventListener("change", function () { s.form.submit(); });
  });

  // Charge category: show default price / unit and require description for custom charges
  document.querySelectorAll("select[data-charge-category]").forEach(function (sel) {
    var form = sel.form;
    var price = form.querySelector("[name=unit_price]");
    var desc = form.querySelector("[name=description]");
    var unit = form.querySelector("[data-unit-label]");
    function update() {
      var opt = sel.options[sel.selectedIndex];
      if (!opt) return;
      if (price) price.placeholder = opt.dataset.price ? "Default " + opt.dataset.price : "";
      if (unit) unit.textContent = opt.dataset.unit ? "per " + opt.dataset.unit : "";
      if (desc) {
        desc.required = opt.dataset.custom === "1";
        desc.placeholder = opt.dataset.custom === "1" ? "Describe the charge (required)" : "Optional – defaults to category name";
      }
    }
    sel.addEventListener("change", update);
    update();
  });

  // Part picker: show price and stock for the chosen part code
  document.querySelectorAll("input[data-part-picker]").forEach(function (inp) {
    var list = document.getElementById(inp.getAttribute("list"));
    var hint = document.getElementById(inp.dataset.partPicker);
    var price = inp.form.querySelector("[name=unit_price], [name=unit_cost]");
    function update() {
      if (!list || !hint) return;
      var code = inp.value.split(" — ")[0].trim().toUpperCase();
      var found = null;
      for (var i = 0; i < list.options.length; i++) {
        if (list.options[i].value.toUpperCase() === code) { found = list.options[i]; break; }
      }
      if (!found) { hint.textContent = ""; return; }
      hint.textContent = found.dataset.name + " · in stock: " + found.dataset.stock + " " + found.dataset.unit + " · price " + found.dataset.price;
      if (price && price.name === "unit_cost") price.placeholder = "Last cost " + found.dataset.cost;
      if (price && price.name === "unit_price") price.placeholder = "Default " + found.dataset.price;
    }
    inp.addEventListener("input", update);
    inp.addEventListener("change", update);
  });

  // "Pay full balance" helper
  document.querySelectorAll("[data-fill]").forEach(function (b) {
    b.addEventListener("click", function () {
      var target = document.getElementById(b.dataset.target);
      if (target) { target.value = b.dataset.fill; target.focus(); }
    });
  });
})();
