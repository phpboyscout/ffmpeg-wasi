// Copy the install command from the home hero.
//
// The button is only useful if it actually copies, so it is wired here rather
// than left as an icon that looks clickable and is not.
document.addEventListener("DOMContentLoaded", function () {
  document.querySelectorAll(".install-copy").forEach(function (button) {
    button.addEventListener("click", function () {
      var box = button.closest(".install-box");
      var command = box && box.querySelector(".install-command");
      if (!command || !navigator.clipboard) {
        return;
      }
      navigator.clipboard.writeText(command.textContent.trim()).then(function () {
        var original = button.textContent;
        button.textContent = "copied";
        button.classList.add("copied");
        setTimeout(function () {
          button.textContent = original;
          button.classList.remove("copied");
        }, 1600);
      });
    });
  });
});
