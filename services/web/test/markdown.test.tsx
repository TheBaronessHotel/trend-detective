import { describe, expect, test } from "bun:test";
import { renderToStaticMarkup } from "react-dom/server";
import Markdown from "../src/components/Markdown";

const html = (text: string) => renderToStaticMarkup(<Markdown text={text} />);

describe("Markdown answers", () => {
  test("bold, lists and inline code", () => {
    const out = html("The trend is **+0.46 °C** per decade.\n\n- one\n- two\n\nSee `p_value`.");
    expect(out).toContain("<strong>+0.46 °C</strong>");
    expect(out).toContain("<ul>");
    expect(out).toContain("<li>two</li>");
    expect(out).toContain("<code>p_value</code>");
  });

  test("tables, which models use for numbers", () => {
    const out = html("| Test | p |\n|---|---|\n| Regression | 0.01 |");
    expect(out).toContain('<div class="md-table"><table>');
    expect(out).toContain("<td>Regression</td>");
  });

  test("headings sit under the panel's own title", () => {
    const out = html("# Summary\n\n## Details");
    expect(out).toContain('<h4 class="md-h">Summary</h4>');
    expect(out).not.toContain("<h1");
  });

  test("links open in a new tab, and unsafe ones lose their target", () => {
    expect(html("[NASA](https://nasa.gov)")).toContain('href="https://nasa.gov" target="_blank" rel="noreferrer noopener"');
    expect(html("[x](javascript:alert(1))")).not.toContain("javascript:");
  });

  test("HTML in an answer is never rendered as HTML", () => {
    const out = html('Look <img src=x onerror="alert(1)"> here <script>alert(1)</script>');
    expect(out).not.toContain("<img");
    expect(out).not.toContain("<script");
  });
});
