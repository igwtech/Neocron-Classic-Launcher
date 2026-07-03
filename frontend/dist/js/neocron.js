$(document).ready(function () {

    let bgm = ['../img/neocron_bg1.png', '../img/neocron_bg2.png'];
    $("body").css("background", "url('" + bgm[Math.floor(Math.random() * bgm.length)] + "') no-repeat center center fixed");
    $("body").css("background-size", "cover");

    $.get("https://neocron.org/api/launcher", function (data) {
        console.log(data);
        let htmlOutput = '<!-- Banners -->';

        if (data.success != "Retrieved successfully.")
            return;

        let groups = data.banner_groups;
        if (!groups || !groups.length)
            return;

        groups.forEach(function (group, index) {
            console.log("Group " + index);
            console.log(" id " + group.id);
            console.log(" order " + group.order);
            console.log(" size " + group.size);
            if (!group.banners || !group.banners.length) {
                console.log(" no banners");
            } else {
                if (group.size == "hero") {
                    //Hero Unit
                    htmlOutput += '<div class="col-5 h-100">';
                } else if (group.size == "regular") {
                    //Regular Unit
                    htmlOutput += '<div class="col-2 h-100">';
                }
                if (group.banners.length > 1) {
                    //Carousel
                    htmlOutput += `
            <div id="carouselExampleControls" class="carousel slide" data-bs-ride="carousel">
                <div class="carousel-inner">
`;
                }
                group.banners.forEach(function (banner, bindex) {
                    console.log(" - action " + banner.action);
                    console.log(" - banner_group " + banner.banner_group);
                    console.log(" - id " + banner.id);
                    console.log(" - image " + banner.image);
                    console.log(" - order " + banner.order);
                    console.log(" - title " + banner.title);
                    console.log(" - type " + banner.type);
                    //Carousel Item
                    if (group.banners.length > 1)
                        htmlOutput += '<div class="carousel-item active">';

                    //Link
                    if (banner.action)
                        htmlOutput += '<a href="' + banner.action + '" target="_blank">';

                    //Image Regular or Hero
                    if (group.size == "regular")
                        htmlOutput += '<img src="' + banner.image + '" ' + ((group.banners.length > 1) ? 'class="d-block w-100"' : '') + ' style="height:300px;width:200px">';
                    if (group.size == "hero")
                        htmlOutput += '<img src="' + banner.image + '" ' + ((group.banners.length > 1) ? 'class="d-block w-100"' : '') + ' style="height:300px;width:460px;">';

                    //Link Closing
                    if (banner.action)
                        htmlOutput += '</a>';

                    //Carousel Item Closing
                    if (group.banners.length > 1)
                        htmlOutput += '</div>';
                });

                //Carousel Closing
                if (group.banners.length > 1)
                    htmlOutput += `
                        </div>
                        <button class="carousel-control-prev" type="button" data-bs-target="#carouselExampleControls" data-bs-slide="prev">
                            <span class="carousel-control-prev-icon" aria-hidden="true"></span>
                            <span class="visually-hidden">Previous</span>
                        </button>
                        <button class="carousel-control-next" type="button" data-bs-target="#carouselExampleControls" data-bs-slide="next">
                            <span class="carousel-control-next-icon" aria-hidden="true"></span>
                            <span class="visually-hidden">Next</span>
                        </button>
                    </div>
                    `;
                
                htmlOutput += '</div>';
            }
        });

        //Master Output
        $('#boxes').html(htmlOutput);
    });
})

//Bypass Filechecks
function giveyouUp() {
    let htmlOutput = `
<video width="100%" height="100%" autoplay>
  <source src="https://ncc-cdn77-mirror.sfo2.cdn.digitaloceanspaces.com/launcher_rsc/videos/nggyu.mp4" type="video/mp4">
Your browser does not support the video tag.
</video>
`;
    $('#launcher').hide();

    $('#filecheck').html(htmlOutput);
    $('#filecheck').show();
}
