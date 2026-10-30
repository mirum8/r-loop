import { View, div } from "gpui-kit";
import { v_flex } from "gpui-base";
import { spaces, terminals, theme, run } from "herdr";

function runs() {
  const tagged = terminals().filter((t) => t.labels?.["rloop.agent"] !== undefined);
  return spaces().flatMap((s) => {
    const own = tagged.filter((t) => t.labels?.["herdr.space"] === s.id);
    if (!own.length) return [];
    const wait = own.map((t) => t.labels["rloop_wait"]).find((v) => v);
    const state = own.map((t) => t.labels["rloop"]).find((v) => v);
    return [{ space: s, wait, state }];
  });
}

export default class Rloop extends View {
  render() {
    const t = theme();
    const rows = runs().map(({ space, wait, state }) =>
      v_flex().id(`rloop-${space.id}`).px(8).py(5).rounded(6).gap(1)
        .when(space.active, (el) => el.bg(t.selected))
        .hover((el) => el.bg(t.hover))
        .on_click(() => run("space.select", { id: space.id }))
        .child(div().truncate().text_color(t.foreground).child(space.name))
        .when(Boolean(wait), (el) => el.child(div().truncate().text_size(11.5).font_bold().text_color("#E0A458").child(wait)))
        .when(!wait && Boolean(state), (el) => el.child(div().truncate().text_size(11.5).text_color("#6E9FC4").child(state))));
    return v_flex().size_full().min_h(0).bg(t.chrome).text_size(13)
      .child(div().h(31).pl(12).flex().items_center().text_size(11.5).text_color(t.dim).child("r-loop"))
      .child(v_flex().flex_1().min_h(0).px(6).pb(10).overflow_y_scrollbar()
        .when(rows.length === 0, (el) => el.child(div().px(8).text_size(11.5).text_color(t.muted).child("no runs")))
        .children(rows));
  }
}
